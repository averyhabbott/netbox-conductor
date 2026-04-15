// Package failover implements automatic NetBox service failover for
// active_standby clusters.
//
// When an active node disconnects, the manager waits a grace period
// (default 30 s). If the node has not reconnected, it dispatches a
// service.start.netbox task to the highest-priority available candidate.
//
// When auto_failback is enabled and a higher-priority node reconnects,
// the manager moves NetBox back to it after the same grace period (giving
// time for the first heartbeat to arrive and confirm the node is healthy).
package failover

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// DefaultGracePeriod is how long the manager waits for a node to reconnect
// before treating the outage as permanent and triggering failover.
const DefaultGracePeriod = 30 * time.Second

// startupSuppressWindow is how long after the manager starts during which
// disconnect events do NOT arm failover timers. This prevents a conductor
// restart from triggering mass failovers as all agents reconnect.
const startupSuppressWindow = 90 * time.Second

// Manager watches for node disconnections and orchestrates failover and
// failback for active_standby clusters.
type Manager struct {
	nodes      *queries.NodeQuerier
	clusters   *queries.ClusterQuerier
	tasks      *queries.TaskResultQuerier
	events     *queries.FailoverEventQuerier
	h          *hub.Hub
	dispatcher *hub.Dispatcher
	broker     *sse.Broker
	grace      time.Duration
	startedAt  time.Time // used for startup suppression window

	mu             sync.Mutex
	failTimers     map[uuid.UUID]*time.Timer // nodeID → pending failover timer
	failbackTimers map[uuid.UUID]*time.Timer // nodeID → pending failback timer
}

// New creates a Manager with the default grace period.
func New(
	nodes *queries.NodeQuerier,
	clusters *queries.ClusterQuerier,
	tasks *queries.TaskResultQuerier,
	events *queries.FailoverEventQuerier,
	h *hub.Hub,
	dispatcher *hub.Dispatcher,
	broker *sse.Broker,
) *Manager {
	return &Manager{
		nodes:          nodes,
		clusters:       clusters,
		tasks:          tasks,
		events:         events,
		h:              h,
		dispatcher:     dispatcher,
		broker:         broker,
		grace:          DefaultGracePeriod,
		startedAt:      time.Now(),
		failTimers:     make(map[uuid.UUID]*time.Timer),
		failbackTimers: make(map[uuid.UUID]*time.Timer),
	}
}

// OnNodeDisconnect must be called whenever an agent WebSocket session closes.
// It cancels any pending failback timer for this node and arms a failover timer.
// The grace period is read from the cluster's failover_delay_secs if available,
// falling back to the manager's default.
func (m *Manager) OnNodeDisconnect(nodeID, clusterID uuid.UUID) {
	// Suppress failover timers during the startup window to avoid mass-triggering
	// when all agents reconnect after a conductor restart.
	if time.Since(m.startedAt) < startupSuppressWindow {
		slog.Debug("failover: startup suppression window active — not arming timer",
			"node", nodeID, "elapsed", time.Since(m.startedAt).Round(time.Second))
		return
	}

	// Look up per-cluster delay; don't hold the mutex during the DB call.
	grace := m.grace
	if cluster, err := m.clusters.GetByID(context.Background(), clusterID); err == nil && cluster.FailoverDelaySecs > 0 {
		grace = time.Duration(cluster.FailoverDelaySecs) * time.Second
	}

	m.mu.Lock()
	// A reconnect-then-disconnect means the failback window closes immediately.
	if t, ok := m.failbackTimers[nodeID]; ok {
		t.Stop()
		delete(m.failbackTimers, nodeID)
	}
	// Arm failover (replace any stale timer for safety).
	if t, ok := m.failTimers[nodeID]; ok {
		t.Stop()
	}
	m.failTimers[nodeID] = time.AfterFunc(grace, func() {
		m.attemptFailover(nodeID, clusterID, true, "disconnect")
	})
	m.mu.Unlock()

	slog.Info("failover: node disconnected — grace period started",
		"node", nodeID, "cluster", clusterID, "grace", grace)
}

// OnNodeConnect must be called whenever an agent WebSocket session is established.
// It cancels any pending failover (the node came back) and arms a failback timer.
func (m *Manager) OnNodeConnect(nodeID, clusterID uuid.UUID) {
	m.mu.Lock()
	if t, ok := m.failTimers[nodeID]; ok {
		t.Stop()
		delete(m.failTimers, nodeID)
		slog.Info("failover: node reconnected within grace period — failover cancelled", "node", nodeID)
	}
	// Arm failback check — wait one grace period to let the first heartbeat land.
	if t, ok := m.failbackTimers[nodeID]; ok {
		t.Stop()
	}
	m.failbackTimers[nodeID] = time.AfterFunc(m.grace, func() {
		m.attemptFailback(nodeID, clusterID)
	})
	m.mu.Unlock()
}

// ── Failover ──────────────────────────────────────────────────────────────────

// attemptFailover is the timer callback for both disconnect-triggered and
// heartbeat-triggered failover.
//
// checkNetboxRunning should be true for disconnect scenarios (the DB still
// reflects the node's last-known state) and false for heartbeat scenarios
// where the heartbeat already updated the DB to netbox_running=false before
// the timer fired.
//
// trigger is a short label stored in the failover_events table:
// "disconnect" | "heartbeat".
func (m *Manager) attemptFailover(failedNodeID, clusterID uuid.UUID, checkNetboxRunning bool, trigger string) {
	m.mu.Lock()
	delete(m.failTimers, failedNodeID)
	m.mu.Unlock()

	ctx := context.Background()

	// Check if the node recovered since the timer was armed.
	// Skip failover only when the node is connected AND reporting NetBox running.
	// (For a disconnect scenario OnNodeConnect would have already cancelled the
	// timer, but guard against the narrow race anyway.)
	if m.h.IsConnected(failedNodeID) {
		if check, err := m.nodes.GetByID(ctx, failedNodeID); err == nil &&
			check.NetboxRunning != nil && *check.NetboxRunning {
			slog.Debug("failover: node recovered before timer fired — skipping", "node", failedNodeID)
			return
		}
	}

	cluster, err := m.clusters.GetByID(ctx, clusterID)
	if err != nil {
		slog.Warn("failover: could not fetch cluster", "cluster", clusterID, "error", err)
		return
	}
	if !cluster.AutoFailover || cluster.Mode != "active_standby" {
		return
	}

	// Only trigger if the failed node was the active one (running NetBox).
	// When checkNetboxRunning is false (heartbeat scenario) we already know it
	// was active — the DB now shows false, which is exactly what we're acting on.
	failedNode, err := m.nodes.GetByID(ctx, failedNodeID)
	if err != nil {
		slog.Warn("failover: could not fetch failed node", "node", failedNodeID, "error", err)
		return
	}
	if checkNetboxRunning && (failedNode.NetboxRunning == nil || !*failedNode.NetboxRunning) {
		slog.Debug("failover: node was not running NetBox — skipping", "node", failedNodeID)
		return
	}

	candidate, err := m.bestCandidate(ctx, clusterID, failedNodeID)
	if err != nil {
		slog.Warn("failover: error selecting candidate", "cluster", clusterID, "error", err)
		return
	}
	if candidate == nil {
		slog.Warn("failover: no suitable candidate available",
			"cluster", clusterID, "failed_node", failedNodeID)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "failover",
			Trigger:        trigger,
			FailedNodeID:   &failedNode.ID,
			FailedNodeName: failedNode.Hostname,
			Success:        false,
			Reason:         "no connected candidate node available",
		})
		m.publish(clusterID, failedNodeID, sse.EventFailoverTriggered, map[string]any{
			"success": false,
			"reason":  "no connected candidate node available",
		})
		return
	}

	slog.Info("failover: dispatching start.netbox to candidate",
		"cluster", clusterID,
		"failed_node", failedNode.Hostname,
		"candidate", candidate.Hostname,
		"priority", candidate.FailoverPriority,
	)

	if err := m.dispatchServiceTask(ctx, candidate, protocol.TaskStartNetbox); err != nil {
		slog.Error("failover: dispatch failed", "candidate", candidate.ID, "error", err)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "failover",
			Trigger:        trigger,
			FailedNodeID:   &failedNode.ID,
			FailedNodeName: failedNode.Hostname,
			TargetNodeID:   &candidate.ID,
			TargetNodeName: candidate.Hostname,
			Success:        false,
			Reason:         "dispatch error: " + err.Error(),
		})
		m.publish(clusterID, failedNodeID, sse.EventFailoverTriggered, map[string]any{
			"success":   false,
			"reason":    "dispatch error: " + err.Error(),
			"candidate": candidate.Hostname,
		})
		return
	}

	// Enqueue stop.netbox for the failed node so it self-stops when it reconnects.
	// This closes the split-brain window: if the node comes back while the candidate
	// is already running NetBox, it will stop itself before accepting connections.
	if err := m.enqueueForReconnect(ctx, failedNode, protocol.TaskStopNetbox); err != nil {
		slog.Warn("failover: could not enqueue stop task for failed node — split-brain risk",
			"node", failedNode.ID, "error", err)
	}

	m.recordEvent(ctx, queries.CreateFailoverEventParams{
		ClusterID:      clusterID,
		EventType:      "failover",
		Trigger:        trigger,
		FailedNodeID:   &failedNode.ID,
		FailedNodeName: failedNode.Hostname,
		TargetNodeID:   &candidate.ID,
		TargetNodeName: candidate.Hostname,
		Success:        true,
	})
	m.publish(clusterID, failedNodeID, sse.EventFailoverTriggered, map[string]any{
		"success":            true,
		"failed_node":        failedNode.Hostname,
		"candidate_node":     candidate.Hostname,
		"candidate_priority": candidate.FailoverPriority,
	})
}

// ── Failback ──────────────────────────────────────────────────────────────────

func (m *Manager) attemptFailback(nodeID, clusterID uuid.UUID) {
	m.mu.Lock()
	delete(m.failbackTimers, nodeID)
	m.mu.Unlock()

	ctx := context.Background()

	if !m.h.IsConnected(nodeID) {
		return // went away again during grace period
	}

	cluster, err := m.clusters.GetByID(ctx, clusterID)
	if err != nil || !cluster.AutoFailback || cluster.Mode != "active_standby" {
		return
	}

	reconnected, err := m.nodes.GetByID(ctx, nodeID)
	if err != nil || reconnected.MaintenanceMode || reconnected.SuppressAutoStart {
		return
	}
	if reconnected.Role == "db_only" {
		return // db_only nodes never run NetBox
	}

	// Find which node is currently the active one (running NetBox and connected).
	nodes, err := m.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		slog.Warn("failback: could not list cluster nodes", "cluster", clusterID, "error", err)
		return
	}

	var currentActive *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.ID == nodeID {
			continue
		}
		if n.NetboxRunning != nil && *n.NetboxRunning && m.h.IsConnected(n.ID) {
			currentActive = n
			break
		}
	}

	if currentActive == nil {
		return // no other node is actively running NetBox — nothing to fail back from
	}
	if reconnected.FailoverPriority <= currentActive.FailoverPriority {
		return // reconnected node is not higher priority
	}

	slog.Info("failback: moving NetBox to higher-priority node",
		"cluster", clusterID,
		"from", currentActive.Hostname, "priority", currentActive.FailoverPriority,
		"to", reconnected.Hostname, "priority", reconnected.FailoverPriority,
	)

	if err := m.dispatchServiceTask(ctx, currentActive, protocol.TaskStopNetbox); err != nil {
		slog.Error("failback: stop dispatch failed", "node", currentActive.ID, "error", err)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "failback",
			Trigger:        "reconnect",
			FailedNodeID:   &currentActive.ID,
			FailedNodeName: currentActive.Hostname,
			TargetNodeID:   &reconnected.ID,
			TargetNodeName: reconnected.Hostname,
			Success:        false,
			Reason:         "stop dispatch failed: " + err.Error(),
		})
		return
	}
	if err := m.dispatchServiceTask(ctx, reconnected, protocol.TaskStartNetbox); err != nil {
		slog.Error("failback: start dispatch failed", "node", reconnected.ID, "error", err)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "failback",
			Trigger:        "reconnect",
			FailedNodeID:   &currentActive.ID,
			FailedNodeName: currentActive.Hostname,
			TargetNodeID:   &reconnected.ID,
			TargetNodeName: reconnected.Hostname,
			Success:        false,
			Reason:         "start dispatch failed: " + err.Error(),
		})
		return
	}

	m.recordEvent(ctx, queries.CreateFailoverEventParams{
		ClusterID:      clusterID,
		EventType:      "failback",
		Trigger:        "reconnect",
		FailedNodeID:   &currentActive.ID,
		FailedNodeName: currentActive.Hostname,
		TargetNodeID:   &reconnected.ID,
		TargetNodeName: reconnected.Hostname,
		Success:        true,
	})
	m.publish(clusterID, nodeID, sse.EventFailbackTriggered, map[string]any{
		"from_node":     currentActive.Hostname,
		"from_priority": currentActive.FailoverPriority,
		"to_node":       reconnected.Hostname,
		"to_priority":   reconnected.FailoverPriority,
	})
}

// ── Heartbeat-triggered failover ──────────────────────────────────────────────

// OnNetboxStopped is called when a heartbeat reports netbox_running transitioning
// from true → false on a connected node. This catches silent crashes that don't
// cause a WebSocket disconnect. The same grace period applies before failover fires,
// giving NetBox a chance to self-recover (e.g. systemd restart policy kicks in).
func (m *Manager) OnNetboxStopped(nodeID, clusterID uuid.UUID) {
	// No startup suppression here — a heartbeat reporting "stopped" is accurate
	// live state, not a false positive from reconnecting agents.
	grace := m.grace
	if cluster, err := m.clusters.GetByID(context.Background(), clusterID); err == nil && cluster.FailoverDelaySecs > 0 {
		grace = time.Duration(cluster.FailoverDelaySecs) * time.Second
	}

	m.mu.Lock()
	if t, ok := m.failbackTimers[nodeID]; ok {
		t.Stop()
		delete(m.failbackTimers, nodeID)
	}
	if t, ok := m.failTimers[nodeID]; ok {
		t.Stop()
	}
	// checkNetboxRunning=false: the DB already reflects the stopped state.
	m.failTimers[nodeID] = time.AfterFunc(grace, func() {
		m.attemptFailover(nodeID, clusterID, false, "heartbeat")
	})
	m.mu.Unlock()

	slog.Info("failover: heartbeat reports NetBox stopped — grace period started",
		"node", nodeID, "cluster", clusterID, "grace", grace)
}

// OnNetboxStarted is called when a heartbeat reports netbox_running transitioning
// from false → true. This cancels any pending failover timer armed by OnNetboxStopped.
func (m *Manager) OnNetboxStarted(nodeID uuid.UUID) {
	m.mu.Lock()
	if t, ok := m.failTimers[nodeID]; ok {
		t.Stop()
		delete(m.failTimers, nodeID)
		slog.Info("failover: NetBox restarted on node — failover cancelled", "node", nodeID)
	}
	m.mu.Unlock()
}

// ── Maintenance-triggered failover ────────────────────────────────────────────

// OnMaintenanceEnabled is called when an operator puts a node into maintenance
// mode. If the cluster has failover_on_maintenance enabled and the node is the
// currently active one (running NetBox), it immediately moves NetBox to the
// best available candidate — no grace period, since this is an explicit action.
func (m *Manager) OnMaintenanceEnabled(nodeID, clusterID uuid.UUID) {
	ctx := context.Background()

	cluster, err := m.clusters.GetByID(ctx, clusterID)
	if err != nil || !cluster.FailoverOnMaintenance || cluster.Mode != "active_standby" {
		return
	}

	node, err := m.nodes.GetByID(ctx, nodeID)
	if err != nil || node.NetboxRunning == nil || !*node.NetboxRunning {
		return // node wasn't running NetBox; nothing to move
	}

	candidate, err := m.bestCandidate(ctx, clusterID, nodeID)
	if err != nil || candidate == nil {
		slog.Warn("failover (maintenance): no connected candidate available",
			"node", nodeID, "cluster", clusterID)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "maintenance_failover",
			Trigger:        "maintenance",
			FailedNodeID:   &node.ID,
			FailedNodeName: node.Hostname,
			Success:        false,
			Reason:         "no connected candidate node available",
		})
		return
	}

	slog.Info("failover (maintenance): moving NetBox off maintenance node",
		"cluster", clusterID,
		"from", node.Hostname,
		"to", candidate.Hostname,
	)

	if err := m.dispatchServiceTask(ctx, node, protocol.TaskStopNetbox); err != nil {
		slog.Error("failover (maintenance): stop dispatch failed", "node", node.ID, "error", err)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "maintenance_failover",
			Trigger:        "maintenance",
			FailedNodeID:   &node.ID,
			FailedNodeName: node.Hostname,
			TargetNodeID:   &candidate.ID,
			TargetNodeName: candidate.Hostname,
			Success:        false,
			Reason:         "stop dispatch failed: " + err.Error(),
		})
		return
	}
	if err := m.dispatchServiceTask(ctx, candidate, protocol.TaskStartNetbox); err != nil {
		slog.Error("failover (maintenance): start dispatch failed", "node", candidate.ID, "error", err)
		m.recordEvent(ctx, queries.CreateFailoverEventParams{
			ClusterID:      clusterID,
			EventType:      "maintenance_failover",
			Trigger:        "maintenance",
			FailedNodeID:   &node.ID,
			FailedNodeName: node.Hostname,
			TargetNodeID:   &candidate.ID,
			TargetNodeName: candidate.Hostname,
			Success:        false,
			Reason:         "start dispatch failed: " + err.Error(),
		})
		return
	}

	m.recordEvent(ctx, queries.CreateFailoverEventParams{
		ClusterID:      clusterID,
		EventType:      "maintenance_failover",
		Trigger:        "maintenance",
		FailedNodeID:   &node.ID,
		FailedNodeName: node.Hostname,
		TargetNodeID:   &candidate.ID,
		TargetNodeName: candidate.Hostname,
		Success:        true,
	})
	m.publish(clusterID, nodeID, sse.EventFailoverTriggered, map[string]any{
		"success":        true,
		"reason":         "maintenance_mode",
		"failed_node":    node.Hostname,
		"candidate_node": candidate.Hostname,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// bestCandidate returns the first connected, eligible node ordered by
// failover_priority DESC (ListByCluster already sorts this way).
func (m *Manager) bestCandidate(ctx context.Context, clusterID, excludeNodeID uuid.UUID) (*queries.Node, error) {
	nodes, err := m.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		n := &nodes[i]
		if n.ID == excludeNodeID {
			continue
		}
		if n.MaintenanceMode || n.SuppressAutoStart || n.Role == "db_only" {
			continue
		}
		if !m.h.IsConnected(n.ID) {
			continue
		}
		return n, nil
	}
	return nil, nil
}

func (m *Manager) dispatchServiceTask(ctx context.Context, node *queries.Node, taskType protocol.TaskType) error {
	taskID := uuid.New()
	dispatchPayload := protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    taskType,
		Params:      json.RawMessage(`{}`),
		TimeoutSecs: 30,
	}
	raw, _ := json.Marshal(dispatchPayload)
	_ = m.tasks.Create(ctx, node.ID, taskID, string(taskType), raw)
	if err := m.dispatcher.Dispatch(node.ID, dispatchPayload); err != nil {
		return err
	}
	_ = m.tasks.SetSent(ctx, taskID)
	return nil
}

// enqueueForReconnect writes a task to the DB in "queued" state without
// attempting to dispatch it over the WebSocket. The agent will pick it up
// the next time it connects via ListPendingByNode.
//
// This is used to send stop.netbox to a node that is currently offline so
// that it self-stops on reconnect, closing the split-brain window.
func (m *Manager) enqueueForReconnect(ctx context.Context, node *queries.Node, taskType protocol.TaskType) error {
	taskID := uuid.New()
	payload := protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    taskType,
		Params:      json.RawMessage(`{}`),
		TimeoutSecs: 30,
	}
	raw, _ := json.Marshal(payload)
	return m.tasks.Create(ctx, node.ID, taskID, string(taskType), raw)
	// Task stays "queued" — picked up and advanced to "sent" on next reconnect.
}

// recordEvent persists a failover event to the database. Errors are logged but
// never returned — a failed write must not block the failover path itself.
func (m *Manager) recordEvent(ctx context.Context, p queries.CreateFailoverEventParams) {
	if m.events == nil {
		return
	}
	if err := m.events.Create(ctx, p); err != nil {
		slog.Warn("failover: failed to record event", "cluster", p.ClusterID, "error", err)
	}
}

func (m *Manager) publish(clusterID, nodeID uuid.UUID, eventType sse.EventType, payload map[string]any) {
	payload["cluster_id"] = clusterID.String()
	payload["node_id"] = nodeID.String()
	m.broker.Publish(sse.Event{
		Type:    eventType,
		NodeID:  nodeID,
		Payload: payload,
	})
}
