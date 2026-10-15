package hub

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

// Hub maintains the set of active agent sessions and provides thread-safe
// access to them. It is the single source of truth for agent connectivity.
type Hub struct {
	mu              sync.RWMutex
	sessions        map[uuid.UUID]*Session // keyed by NodeID (assigned agents)
	stagingSessions map[uuid.UUID]*Session // keyed by StagingAgentID (unassigned agents)

	taskWaitersMu sync.Mutex
	taskWaiters   map[uuid.UUID]*taskWatch
}

// taskWatch waits for one task's result. With node set it also follows that
// node's heartbeats, which list every task the agent holds: once the agent has
// shown it holds the task (ack or heartbeat), a heartbeat without it means the
// task ended and its result never arrived.
type taskWatch struct {
	node     uuid.UUID
	result   chan protocol.TaskResultPayload
	known    atomic.Bool
	lost     chan struct{}
	lostOnce sync.Once
}

// New creates an empty Hub.
func New() *Hub {
	return &Hub{
		sessions:        make(map[uuid.UUID]*Session),
		stagingSessions: make(map[uuid.UUID]*Session),
		taskWaiters:     make(map[uuid.UUID]*taskWatch),
	}
}

// Register adds a session. If a session for the same NodeID already exists
// (stale connection), it is evicted first: its send channel is closed so the
// write pump exits, and the WebSocket is force-closed so readPump exits too.
func (h *Hub) Register(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.sessions[s.NodeID]; ok {
		close(old.send) // signal the old write pump to exit
		old.Close()     // force readPump to exit so connectNode can clean up
	}
	h.sessions[s.NodeID] = s
}

// Unregister removes a session by NodeID. Used for forced removal (e.g. node
// deletion). For normal session teardown in connectNode, use UnregisterIfSame
// to avoid accidentally evicting a replacement session.
func (h *Hub) Unregister(nodeID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[nodeID]; ok {
		close(s.send)
		delete(h.sessions, nodeID)
	}
}

// UnregisterIfSame removes the session for nodeID only when the currently
// registered session is the same object as s. This prevents a connectNode
// defer from evicting a replacement session that was registered after this
// one was evicted by Hub.Register. Returns false when a newer session has
// replaced s, so the caller can skip its disconnect side effects.
func (h *Hub) UnregisterIfSame(nodeID uuid.UUID, s *Session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.sessions[nodeID]; ok && existing == s {
		delete(h.sessions, nodeID)
		// send is already closed (by Register's eviction or WritePump exit).
		// pumpCtx will be cancelled by connectNode's defer pumpCancel().
		return true
	}
	return false
}

// Get returns the active session for a node, or nil if not connected.
func (h *Hub) Get(nodeID uuid.UUID) *Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[nodeID]
}

// IsConnected reports whether a node currently has an active session.
func (h *Hub) IsConnected(nodeID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.sessions[nodeID]
	return ok
}

// ConnectedCount returns the number of currently-connected assigned agents.
func (h *Hub) ConnectedCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.sessions)
}

// ConnectedStagingCount returns the number of currently-connected staging agents.
func (h *Hub) ConnectedStagingCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.stagingSessions)
}

// ConnectedNodeIDs returns the IDs of all currently-connected nodes.
func (h *Hub) ConnectedNodeIDs() []uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]uuid.UUID, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// DrainAll closes all active sessions. Called during graceful shutdown so agents
// receive a WebSocket close frame and reconnect to the new instance.
func (h *Hub) DrainAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for nodeID, s := range h.sessions {
		close(s.send)
		delete(h.sessions, nodeID)
	}
	for stagingID, s := range h.stagingSessions {
		close(s.send)
		delete(h.stagingSessions, stagingID)
	}
}

// ── Staging session methods ───────────────────────────────────────────────────

// RegisterStaging adds a staging agent session (unassigned agents).
func (h *Hub) RegisterStaging(stagingID uuid.UUID, s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.stagingSessions[stagingID]; ok {
		close(old.send)
	}
	h.stagingSessions[stagingID] = s
}

// UnregisterStaging removes a staging session.
func (h *Hub) UnregisterStaging(stagingID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.stagingSessions[stagingID]; ok {
		close(s.send)
		delete(h.stagingSessions, stagingID)
	}
}

// IsConnectedStaging reports whether a staging agent has an active session.
func (h *Hub) IsConnectedStaging(stagingID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.stagingSessions[stagingID]
	return ok
}

// NetboxVersionForCluster returns the NetBox version reported by any connected
// node that belongs to clusterID, or "" if none are connected / reporting.
func (h *Hub) NetboxVersionForCluster(clusterID uuid.UUID) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.sessions {
		if s.ClusterID == clusterID && s.NetboxVersion != "" {
			return s.NetboxVersion
		}
	}
	return ""
}

// UnregisterCluster closes and removes all sessions that belong to clusterID.
// Used during cluster deletion to disconnect every agent at once.
func (h *Hub) UnregisterCluster(clusterID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for nodeID, s := range h.sessions {
		if s.ClusterID == clusterID {
			close(s.send)
			delete(h.sessions, nodeID)
		}
	}
}

// ── Synchronous task result waiting ─────────────────────────────────────────

// WaitForTask blocks until the agent posts a result for taskID or the context /
// timeout fires. Callers register before dispatching the task to avoid a race.
func (h *Hub) WaitForTask(ctx context.Context, taskID uuid.UUID, timeout time.Duration) (*protocol.TaskResultPayload, error) {
	ch, done := h.ExpectTask(taskID)
	defer done()

	select {
	case result := <-ch:
		return &result, nil
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ExpectTask registers for taskID's result; call it before dispatching the
// task so a fast result can't be missed, and call done when no longer waiting.
func (h *Hub) ExpectTask(taskID uuid.UUID) (result <-chan protocol.TaskResultPayload, done func()) {
	w, done := h.watchTask(taskID, uuid.Nil)
	return w.result, done
}

// watchTask registers a watch for taskID on node; see taskWatch.
func (h *Hub) watchTask(taskID, node uuid.UUID) (*taskWatch, func()) {
	w := &taskWatch{node: node, result: make(chan protocol.TaskResultPayload, 1), lost: make(chan struct{})}
	h.taskWaitersMu.Lock()
	h.taskWaiters[taskID] = w
	h.taskWaitersMu.Unlock()
	return w, func() {
		h.taskWaitersMu.Lock()
		delete(h.taskWaiters, taskID)
		h.taskWaitersMu.Unlock()
	}
}

// NotifyTaskResult delivers a task result to any registered waiter.
// Called by handleTaskResult after the result is persisted.
func (h *Hub) NotifyTaskResult(taskID uuid.UUID, result protocol.TaskResultPayload) {
	h.taskWaitersMu.Lock()
	w, ok := h.taskWaiters[taskID]
	h.taskWaitersMu.Unlock()
	if ok {
		select {
		case w.result <- result:
		default:
		}
	}
}

// NotifyTaskAck records that the agent received taskID.
func (h *Hub) NotifyTaskAck(taskID uuid.UUID) {
	h.taskWaitersMu.Lock()
	defer h.taskWaitersMu.Unlock()
	if w, ok := h.taskWaiters[taskID]; ok {
		w.known.Store(true)
	}
}

// NotifyHeartbeat checks node's watched tasks against the tasks its heartbeat
// lists. The agent sends acks, results and heartbeats in order, so a listed
// task the agent no longer lists has had its result handled already, if it sent one.
func (h *Hub) NotifyHeartbeat(node uuid.UUID, tasks []string) {
	h.taskWaitersMu.Lock()
	defer h.taskWaitersMu.Unlock()
	for id, w := range h.taskWaiters {
		if w.node != node {
			continue
		}
		if slices.Contains(tasks, id.String()) {
			w.known.Store(true)
		} else if w.known.Load() {
			w.lostOnce.Do(func() { close(w.lost) })
		}
	}
}

