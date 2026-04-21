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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	ev "github.com/averyhabbott/netbox-conductor/internal/server/events"

	"github.com/averyhabbott/netbox-conductor/internal/server/configgen"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/nodestate"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// DefaultGracePeriod is how long the manager waits for a node to reconnect
// before treating the outage as permanent and triggering failover.
const DefaultGracePeriod = 30 * time.Second

// DefaultFailbackMultiplier is multiplied by the cluster's failover_delay_secs
// to derive the failback stability window. Default 3 → 90s at 30s failover delay.
const DefaultFailbackMultiplier = 3

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
	emitter    ev.Emitter
	h          *hub.Hub
	dispatcher *hub.Dispatcher
	broker     *sse.Broker
	creds      *queries.CredentialQuerier
	enc        *crypto.Encryptor
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
	emitter ev.Emitter,
	h *hub.Hub,
	dispatcher *hub.Dispatcher,
	broker *sse.Broker,
	creds *queries.CredentialQuerier,
	enc *crypto.Encryptor,
) *Manager {
	return &Manager{
		nodes:          nodes,
		clusters:       clusters,
		tasks:          tasks,
		emitter:        emitter,
		h:              h,
		dispatcher:     dispatcher,
		broker:         broker,
		creds:          creds,
		enc:            enc,
		grace:          DefaultGracePeriod,
		startedAt:      time.Now(),
		failTimers:     make(map[uuid.UUID]*time.Timer),
		failbackTimers: make(map[uuid.UUID]*time.Timer),
	}
}

// haEventParams carries the fields needed to emit an NBC-HA-* event.
type haEventParams struct {
	ClusterID      uuid.UUID
	EventType      string // "failover" | "failback" | "maintenance_failover"
	Trigger        string // "disconnect" | "heartbeat" | "reconnect" | "maintenance"
	FailedNodeID   *uuid.UUID
	FailedNodeName string
	TargetNodeID   *uuid.UUID
	TargetNodeName string
	Success        bool
	Reason         string
}

// emitHAEvent converts a haEventParams into the appropriate NBC-HA-* event and emits it.
func (m *Manager) emitHAEvent(_ context.Context, p haEventParams) {
	code := ev.CodeFailoverInitiated
	severity := ev.SeverityWarn
	switch {
	case p.EventType == "failover_initiated":
		// code/severity stay as CodeFailoverInitiated/SeverityWarn
	case p.EventType == "failover" && p.Success:
		code, severity = ev.CodeFailoverCompleted, ev.SeverityInfo
	case p.EventType == "failover" && !p.Success:
		code, severity = ev.CodeFailoverFailed, ev.SeverityError
	case p.EventType == "failback_initiated":
		code, severity = ev.CodeFailbackInitiated, ev.SeverityInfo
	case p.EventType == "failback" && p.Success:
		code, severity = ev.CodeFailbackCompleted, ev.SeverityInfo
	case p.EventType == "failback" && !p.Success:
		code, severity = ev.CodeFailoverFailed, ev.SeverityError
	case p.EventType == "maintenance_failover":
		code = ev.CodeMaintenanceFailover
	}

	var parts []string
	if p.FailedNodeName != "" {
		parts = append(parts, "from="+p.FailedNodeName)
	}
	if p.TargetNodeName != "" {
		parts = append(parts, "to="+p.TargetNodeName)
	}
	if p.Trigger != "" {
		parts = append(parts, "trigger="+p.Trigger)
	}
	if p.Reason != "" {
		parts = append(parts, "reason="+p.Reason)
	}
	msg := strings.Join(parts, " ")
	if msg == "" {
		msg = p.EventType
	}

	nodeID := p.FailedNodeID
	if nodeID == nil {
		nodeID = p.TargetNodeID
	}

	b := ev.New(ev.CategoryHA, severity, code, msg, ev.ActorSystem).Cluster(p.ClusterID)
	if nodeID != nil {
		b = b.Node(*nodeID)
	}
	m.emitter.Emit(b.Build())
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
// The failback delay is failover_delay_secs × failback_multiplier (default 90s).
func (m *Manager) OnNodeConnect(nodeID, clusterID uuid.UUID) {
	m.mu.Lock()
	if t, ok := m.failTimers[nodeID]; ok {
		t.Stop()
		delete(m.failTimers, nodeID)
		slog.Info("failover: node reconnected within grace period — failover cancelled", "node", nodeID)
	}
	if t, ok := m.failbackTimers[nodeID]; ok {
		t.Stop()
		delete(m.failbackTimers, nodeID)
	}
	m.mu.Unlock()

	// Read per-cluster settings outside the lock to avoid holding it during a DB call.
	grace := m.grace
	multiplier := DefaultFailbackMultiplier
	if cluster, err := m.clusters.GetByID(context.Background(), clusterID); err == nil {
		if cluster.FailoverDelaySecs > 0 {
			grace = time.Duration(cluster.FailoverDelaySecs) * time.Second
		}
		if cluster.FailbackMultiplier > 0 {
			multiplier = cluster.FailbackMultiplier
		}
	}
	failbackDelay := grace * time.Duration(multiplier)

	m.mu.Lock()
	m.failbackTimers[nodeID] = time.AfterFunc(failbackDelay, func() {
		m.attemptFailback(nodeID, clusterID)
	})
	m.mu.Unlock()

	slog.Info("failover: node connected — failback check scheduled",
		"node", nodeID, "cluster", clusterID, "failback_delay", failbackDelay)
}

// OnMaintenanceDisabled must be called when an operator removes a node from
// maintenance mode. Arms a failback timer so the node can reclaim its role
// as the highest-priority active node after a stability window.
func (m *Manager) OnMaintenanceDisabled(nodeID, clusterID uuid.UUID) {
	// Reset any stale failback timer for a fresh stability window.
	m.mu.Lock()
	if t, ok := m.failbackTimers[nodeID]; ok {
		t.Stop()
		delete(m.failbackTimers, nodeID)
	}
	m.mu.Unlock()

	grace := m.grace
	multiplier := DefaultFailbackMultiplier
	if cluster, err := m.clusters.GetByID(context.Background(), clusterID); err == nil {
		if cluster.FailoverDelaySecs > 0 {
			grace = time.Duration(cluster.FailoverDelaySecs) * time.Second
		}
		if cluster.FailbackMultiplier > 0 {
			multiplier = cluster.FailbackMultiplier
		}
	}
	failbackDelay := grace * time.Duration(multiplier)

	m.mu.Lock()
	m.failbackTimers[nodeID] = time.AfterFunc(failbackDelay, func() {
		m.attemptFailback(nodeID, clusterID)
	})
	m.mu.Unlock()

	slog.Info("failover: maintenance disabled — failback check scheduled",
		"node", nodeID, "cluster", clusterID, "failback_delay", failbackDelay)
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
		m.emitHAEvent(ctx, haEventParams{
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

	m.emitHAEvent(ctx, haEventParams{
		ClusterID:      clusterID,
		EventType:      "failover_initiated",
		Trigger:        trigger,
		FailedNodeID:   &failedNode.ID,
		FailedNodeName: failedNode.Hostname,
		TargetNodeID:   &candidate.ID,
		TargetNodeName: candidate.Hostname,
	})

	// Trigger a Patroni switchover so the DB primary follows the new active node.
	// Fetch all nodes for the switchover decision (bestCandidate already fetched them
	// internally; this second call is acceptable on this rare, critical path).
	allNodes, _ := m.nodes.ListByCluster(ctx, clusterID)
	switchoverTriggered, switchoverErr := m.triggerPatroniSwitchover(ctx, cluster, candidate, allNodes, failedNodeID)
	if switchoverErr != nil {
		slog.Warn("failover: patroni switchover failed — NetBox start may fail if DB is read-only",
			"cluster", clusterID, "error", switchoverErr)
		// Do not abort — NetBox failover proceeds regardless.
	}
	if switchoverTriggered && cluster.AppTierAlwaysAvailable {
		candidateIP, _, _ := strings.Cut(candidate.IPAddress, "/")
		m.dispatchDBHostUpdate(ctx, allNodes, candidateIP)
		m.dispatchSentinelUpdate(ctx, cluster, allNodes, candidateIP)
	}

	if err := m.dispatchServiceTask(ctx, candidate, protocol.TaskStartNetbox); err != nil {
		slog.Error("failover: dispatch failed", "candidate", candidate.ID, "error", err)
		m.emitHAEvent(ctx, haEventParams{
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

	m.emitHAEvent(ctx, haEventParams{
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

	// For heartbeat-triggered failovers, the failed node's agent stays connected
	// throughout (only NetBox stopped, not the WebSocket). OnNodeConnect is never
	// called again, so the failback timer never gets re-armed via the normal path.
	// Re-arm it here so the node can reclaim active status once it's stable.
	if m.h.IsConnected(failedNodeID) {
		m.OnNodeConnect(failedNodeID, clusterID)
	}
}

// ── Failback ──────────────────────────────────────────────────────────────────

func (m *Manager) attemptFailback(nodeID, clusterID uuid.UUID) {
	m.mu.Lock()
	delete(m.failbackTimers, nodeID)
	m.mu.Unlock()

	ctx := context.Background()

	if !m.h.IsConnected(nodeID) {
		return // went away again during stability window
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
		return
	}

	nodes, err := m.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		slog.Warn("failback: could not list cluster nodes", "cluster", clusterID, "error", err)
		return
	}

	if cluster.AppTierAlwaysAvailable {
		m.attemptFailbackAlwaysAvailable(ctx, cluster, reconnected, nodes)
	} else {
		m.attemptFailbackActiveStandby(ctx, cluster, reconnected, nodes)
	}
}

// attemptFailbackAlwaysAvailable handles failback when app_tier_always_available=true.
// All eligible app/HC nodes should always be running NetBox; no running node is ever
// stopped. Actions:
//   - If the reconnected node has higher priority than the current Patroni primary,
//     trigger a switchover. The TypePatroniState cascade (dispatchDBHostUpdate with
//     RestartAfter=true) will restart all nodes and start the reconnected node.
//   - Otherwise, ensure the reconnected node is started with a fresh DB host config.
func (m *Manager) attemptFailbackAlwaysAvailable(
	ctx context.Context,
	cluster *queries.Cluster,
	reconnected *queries.Node,
	nodes []queries.Node,
) {
	// Find the current Patroni primary (excluding the reconnected node itself).
	var currentPrimary *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.ID == reconnected.ID {
			continue
		}
		role := nodestate.ExtractPatroniRole(n.PatroniState)
		if role == "primary" || role == "master" {
			currentPrimary = n
			break
		}
	}

	needsSwitchover := currentPrimary != nil &&
		reconnected.FailoverPriority > currentPrimary.FailoverPriority

	if needsSwitchover {
		slog.Info("failback (always-available): moving DB primary to higher-priority node",
			"cluster", cluster.ID,
			"from", currentPrimary.Hostname, "priority", currentPrimary.FailoverPriority,
			"to", reconnected.Hostname, "priority", reconnected.FailoverPriority,
		)

		reconnectedIP, _, _ := strings.Cut(reconnected.IPAddress, "/")
		switchoverTriggered, switchoverErr := m.triggerPatroniSwitchover(ctx, cluster, reconnected, nodes, currentPrimary.ID)
		if switchoverErr != nil {
			slog.Warn("failback (always-available): patroni switchover failed",
				"cluster", cluster.ID, "error", switchoverErr)
		}
		if switchoverTriggered {
			// Fast-path: update DATABASE.HOST on all nodes (RestartAfter=false).
			// The TypePatroniState cascade in the agent handler fires when the new
			// primary reports its role, dispatching TaskUpdateDBHost{RestartAfter=true}
			// to all nodes — this restarts running nodes and starts the reconnected one.
			m.dispatchDBHostUpdate(ctx, nodes, reconnectedIP)
			m.dispatchSentinelUpdate(ctx, cluster, nodes, reconnectedIP)
		}
		m.emitHAEvent(ctx, haEventParams{
			ClusterID:      cluster.ID,
			EventType:      "failback",
			Trigger:        "reconnect",
			FailedNodeID:   &currentPrimary.ID,
			FailedNodeName: currentPrimary.Hostname,
			TargetNodeID:   &reconnected.ID,
			TargetNodeName: reconnected.Hostname,
			Success:        switchoverTriggered,
		})
		m.publish(cluster.ID, reconnected.ID, sse.EventFailbackTriggered, map[string]any{
			"from_node":     currentPrimary.Hostname,
			"from_priority": currentPrimary.FailoverPriority,
			"to_node":       reconnected.Hostname,
			"to_priority":   reconnected.FailoverPriority,
		})
		return
	}

	// No switchover needed. If the reconnected node's NetBox is stopped (e.g. it
	// received a TaskStopNetbox for split-brain prevention during a prior failover),
	// start it. Update DATABASE.HOST first — it may have a stale value from when it
	// was offline during the prior failover's dispatchDBHostUpdate.
	if reconnected.NetboxRunning != nil && *reconnected.NetboxRunning {
		return // already running — nothing to do
	}

	if currentPrimary != nil {
		primaryIP, _, _ := strings.Cut(currentPrimary.IPAddress, "/")
		m.dispatchDBHostUpdate(ctx, []queries.Node{*reconnected}, primaryIP)
	}
	if err := m.dispatchServiceTask(ctx, reconnected, protocol.TaskStartNetbox); err != nil {
		slog.Error("failback (always-available): start dispatch failed",
			"node", reconnected.ID, "error", err)
		return
	}
	slog.Info("failback (always-available): started NetBox on reconnected node",
		"cluster", cluster.ID, "node", reconnected.Hostname)
}

// attemptFailbackActiveStandby handles failback for standard active/standby clusters
// (app_tier_always_available=false). Exactly one node runs NetBox at a time.
// If the reconnected node has higher priority than the current active, NetBox
// is moved back to it.
func (m *Manager) attemptFailbackActiveStandby(
	ctx context.Context,
	cluster *queries.Cluster,
	reconnected *queries.Node,
	nodes []queries.Node,
) {
	// Find which node is currently the active one (running NetBox and connected).
	var currentActive *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.ID == reconnected.ID {
			continue
		}
		if n.NetboxRunning != nil && *n.NetboxRunning && m.h.IsConnected(n.ID) {
			currentActive = n
			break
		}
	}

	if currentActive == nil {
		// No node is running NetBox. If auto-failover is on and the cluster is not
		// in a restore, start NetBox on the highest-priority connected node — this
		// recovers the cluster after a conductor restart where the previous start
		// task was never acknowledged (e.g. conductor was restarted mid-dispatch).
		if !cluster.AutoFailover {
			return
		}
		var best *queries.Node
		for i := range nodes {
			n := &nodes[i]
			if !m.h.IsConnected(n.ID) {
				continue
			}
			if best == nil || n.FailoverPriority > best.FailoverPriority {
				best = n
			}
		}
		if best == nil {
			return
		}
		slog.Info("failover: no node running NetBox — starting on highest-priority connected node",
			"cluster", cluster.ID, "node", best.Hostname, "priority", best.FailoverPriority)
		if err := m.dispatchServiceTask(ctx, best, protocol.TaskStartNetbox); err != nil {
			slog.Error("failover: orphan-recovery start dispatch failed", "node", best.ID, "error", err)
		}
		return
	}
	if reconnected.FailoverPriority <= currentActive.FailoverPriority {
		return // reconnected node is not higher priority
	}

	m.emitHAEvent(ctx, haEventParams{
		ClusterID:      cluster.ID,
		EventType:      "failback_initiated",
		Trigger:        "reconnect",
		TargetNodeID:   &reconnected.ID,
		TargetNodeName: reconnected.Hostname,
	})

	slog.Info("failback: moving NetBox to higher-priority node",
		"cluster", cluster.ID,
		"from", currentActive.Hostname, "priority", currentActive.FailoverPriority,
		"to", reconnected.Hostname, "priority", reconnected.FailoverPriority,
	)

	switchoverTriggered, switchoverErr := m.triggerPatroniSwitchover(ctx, cluster, reconnected, nodes, currentActive.ID)
	if switchoverErr != nil {
		slog.Warn("failback: patroni switchover failed", "cluster", cluster.ID, "error", switchoverErr)
	}
	if switchoverTriggered && cluster.AppTierAlwaysAvailable {
		// Should not reach here (AppTierAlwaysAvailable is handled by the other branch),
		// but guard for safety.
		reconnectedIP, _, _ := strings.Cut(reconnected.IPAddress, "/")
		m.dispatchDBHostUpdate(ctx, nodes, reconnectedIP)
		m.dispatchSentinelUpdate(ctx, cluster, nodes, reconnectedIP)
	}

	if err := m.dispatchServiceTask(ctx, currentActive, protocol.TaskStopNetbox); err != nil {
		slog.Error("failback: stop dispatch failed", "node", currentActive.ID, "error", err)
		m.emitHAEvent(ctx, haEventParams{
			ClusterID:      cluster.ID,
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
		m.emitHAEvent(ctx, haEventParams{
			ClusterID:      cluster.ID,
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

	m.emitHAEvent(ctx, haEventParams{
		ClusterID:      cluster.ID,
		EventType:      "failback",
		Trigger:        "reconnect",
		FailedNodeID:   &currentActive.ID,
		FailedNodeName: currentActive.Hostname,
		TargetNodeID:   &reconnected.ID,
		TargetNodeName: reconnected.Hostname,
		Success:        true,
	})
	m.publish(cluster.ID, reconnected.ID, sse.EventFailbackTriggered, map[string]any{
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
		m.emitHAEvent(ctx, haEventParams{
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

	// Trigger a Patroni switchover so the DB primary follows the candidate.
	maintenanceNodes, _ := m.nodes.ListByCluster(ctx, clusterID)
	switchoverTriggered, switchoverErr := m.triggerPatroniSwitchover(ctx, cluster, candidate, maintenanceNodes, nodeID)
	if switchoverErr != nil {
		slog.Warn("failover (maintenance): patroni switchover failed", "error", switchoverErr)
	}
	if switchoverTriggered && cluster.AppTierAlwaysAvailable {
		candidateIP, _, _ := strings.Cut(candidate.IPAddress, "/")
		m.dispatchDBHostUpdate(ctx, maintenanceNodes, candidateIP)
		m.dispatchSentinelUpdate(ctx, cluster, maintenanceNodes, candidateIP)
	}

	if err := m.dispatchServiceTask(ctx, node, protocol.TaskStopNetbox); err != nil {
		slog.Error("failover (maintenance): stop dispatch failed", "node", node.ID, "error", err)
		m.emitHAEvent(ctx, haEventParams{
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
		m.emitHAEvent(ctx, haEventParams{
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

	m.emitHAEvent(ctx, haEventParams{
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

// bestCandidate returns the best connected, eligible node ordered by
// failover_priority DESC (ListByCluster already sorts this way).
// Healthy candidates are preferred over unhealthy ones within each priority tier.
func (m *Manager) bestCandidate(ctx context.Context, clusterID, excludeNodeID uuid.UUID) (*queries.Node, error) {
	nodes, err := m.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	// Collect eligible candidates (connected, not excluded, not maintenance/suppressed, not db_only).
	var eligible []*queries.Node
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
		eligible = append(eligible, n)
	}
	if len(eligible) == 0 {
		return nil, nil
	}
	// Prefer healthy candidates. Nodes are already sorted by failover_priority DESC,
	// so the first healthy one is both the highest priority and healthy.
	for _, n := range eligible {
		patroniRole := nodestate.ExtractPatroniRole(n.PatroniState)
		if nodestate.ComputeNodeHealth(n.Role, "connected", n.NetboxRunning, n.RQRunning, patroniRole, false) == "healthy" {
			return n, nil
		}
	}
	return eligible[0], nil // fallback: best priority regardless of health
}

// patroniRestCreds fetches and decrypts the Patroni REST API credentials for a cluster.
// Returns empty strings if no credentials are found (e.g. Patroni not yet configured).
func (m *Manager) patroniRestCreds(ctx context.Context, clusterID uuid.UUID) (user, pass string) {
	if m.creds == nil || m.enc == nil {
		return
	}
	cred, err := m.creds.GetByKind(ctx, clusterID, "patroni_rest_password")
	if err != nil {
		return
	}
	user = cred.Username
	if pw, err := m.enc.Decrypt(cred.PasswordEnc); err == nil {
		pass = string(pw)
	}
	return
}

// triggerPatroniSwitchover calls the Patroni REST API to move the primary to the candidate node.
//
// It is a no-op when:
//   - the cluster does not use Patroni (PatroniConfigured=false)
//   - the cluster mode is not active_standby
//   - the candidate is already the Patroni primary (no switchover needed)
//   - the candidate has no Patroni role (app+db_only topology — Patroni is on db_only nodes)
//
// When the current primary is still reachable, POST /switchover is called on it (graceful).
// When the primary is unreachable, POST /failover is called on the candidate (forced).
func (m *Manager) triggerPatroniSwitchover(
	ctx context.Context,
	cluster *queries.Cluster,
	candidate *queries.Node,
	nodes []queries.Node,
	failingNodeID uuid.UUID,
) (triggered bool, err error) {
	if !cluster.PatroniConfigured || cluster.Mode != "active_standby" {
		return false, nil
	}
	candidateRole := nodestate.ExtractPatroniRole(candidate.PatroniState)
	// Candidate is already primary, or has no Patroni role (app-only node) — nothing to do.
	if candidateRole == "primary" || candidateRole == "master" || candidateRole == "" {
		return false, nil
	}

	// Find the connected Patroni primary. We do NOT exclude failingNodeID here:
	// in the heartbeat path (NetBox stopped but agent still connected) the failing
	// node may still be the Patroni primary and capable of a graceful switchover.
	// Truly disconnected nodes are already excluded by IsConnected.
	var primary *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if !m.h.IsConnected(n.ID) {
			continue
		}
		if r := nodestate.ExtractPatroniRole(n.PatroniState); r == "primary" || r == "master" {
			primary = n
			break
		}
	}

	restUser, restPass := m.patroniRestCreds(ctx, cluster.ID)

	if primary != nil {
		// Graceful switchover: POST /switchover to the current primary's REST API.
		primaryIP, _, _ := strings.Cut(primary.IPAddress, "/")
		body, _ := json.Marshal(map[string]string{
			"leader":    primary.Hostname,
			"candidate": candidate.Hostname,
		})
		url := fmt.Sprintf("http://%s:8008/switchover", primaryIP)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(restUser, restPass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("patroni switchover request: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			out, _ := io.ReadAll(resp.Body)
			return false, fmt.Errorf("patroni switchover returned %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
		}
		slog.Info("failover: patroni switchover triggered via REST API",
			"from", primary.Hostname, "to", candidate.Hostname)
	} else {
		// No connected primary — forced failover via candidate's REST API.
		failingHostname := failingNodeID.String()
		for i := range nodes {
			if nodes[i].ID == failingNodeID {
				failingHostname = nodes[i].Hostname
				break
			}
		}
		candidateIP, _, _ := strings.Cut(candidate.IPAddress, "/")
		body, _ := json.Marshal(map[string]string{
			"master":    failingHostname,
			"candidate": candidate.Hostname,
		})
		url := fmt.Sprintf("http://%s:8008/failover", candidateIP)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(restUser, restPass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("patroni failover request: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			out, _ := io.ReadAll(resp.Body)
			return false, fmt.Errorf("patroni failover returned %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
		}
		slog.Info("failover: patroni forced failover triggered via REST API",
			"dead_primary", failingHostname, "candidate", candidate.Hostname)
	}
	return true, nil
}

// dispatchDBHostUpdate sends a TaskUpdateDBHost task to all currently connected
// cluster nodes. This is called after a Patroni switchover when
// AppTierAlwaysAvailable=true so every node updates its DATABASE_HOST to point
// at the new Patroni primary.
func (m *Manager) dispatchDBHostUpdate(ctx context.Context, nodes []queries.Node, newHost string) {
	params, _ := json.Marshal(protocol.DBHostUpdateParams{Host: newHost, RestartAfter: false})
	for _, n := range nodes {
		if !m.h.IsConnected(n.ID) {
			continue
		}
		taskID := uuid.New()
		_ = m.tasks.Create(ctx, n.ID, taskID, string(protocol.TaskUpdateDBHost), params)
		if err := m.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskUpdateDBHost,
			Params:      json.RawMessage(params),
			TimeoutSecs: 30,
		}); err != nil {
			slog.Warn("failover: db-host-update dispatch failed", "node", n.Hostname, "error", err)
			continue
		}
		_ = m.tasks.SetSent(ctx, taskID)
	}
}

// dispatchSentinelUpdate re-renders sentinel.conf on all connected cluster nodes
// with newMasterIP as the monitored Redis master. Called after a Patroni switchover
// when AppTierAlwaysAvailable=true so Redis Sentinel stays in sync with the active node.
func (m *Manager) dispatchSentinelUpdate(ctx context.Context, cluster *queries.Cluster, nodes []queries.Node, newMasterIP string) {
	if cluster.RedisSentinelMaster == "" {
		return // sentinel not configured for this cluster
	}
	redisPassword := ""
	if m.creds != nil && m.enc != nil {
		if cred, err := m.creds.GetByKind(ctx, cluster.ID, "redis_tasks_password"); err == nil {
			if pw, err := m.enc.Decrypt(cred.PasswordEnc); err == nil {
				redisPassword = string(pw)
			}
		}
	}
	for _, n := range nodes {
		if !m.h.IsConnected(n.ID) {
			continue
		}
		nodeIP, _, _ := strings.Cut(n.IPAddress, "/")
		content, sha256hex, err := configgen.RenderSentinel(configgen.SentinelInput{
			Scope:      cluster.RedisSentinelMaster,
			MasterHost: newMasterIP,
			BindAddr:   nodeIP,
			Password:   redisPassword,
		})
		if err != nil {
			slog.Warn("failover: sentinel config render failed", "node", n.Hostname, "error", err)
			continue
		}
		params, _ := json.Marshal(protocol.SentinelConfigWriteParams{
			Content:      content,
			Sha256:       sha256hex,
			RestartAfter: true,
		})
		taskID := uuid.New()
		_ = m.tasks.Create(ctx, n.ID, taskID, string(protocol.TaskWriteSentinelConf), params)
		if err := m.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskWriteSentinelConf,
			Params:      json.RawMessage(params),
			TimeoutSecs: 30,
		}); err != nil {
			slog.Warn("failover: sentinel config dispatch failed", "node", n.Hostname, "error", err)
			continue
		}
		_ = m.tasks.SetSent(ctx, taskID)
	}
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


func (m *Manager) publish(clusterID, nodeID uuid.UUID, eventType sse.EventType, payload map[string]any) {
	payload["cluster_id"] = clusterID.String()
	payload["node_id"] = nodeID.String()
	m.broker.Publish(sse.Event{
		Type:    eventType,
		NodeID:  nodeID,
		Payload: payload,
	})
}
