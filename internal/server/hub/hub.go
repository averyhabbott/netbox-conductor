package hub

import (
	"context"
	"sync"
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
	taskWaiters   map[uuid.UUID]chan protocol.TaskResultPayload
}

// New creates an empty Hub.
func New() *Hub {
	return &Hub{
		sessions:        make(map[uuid.UUID]*Session),
		stagingSessions: make(map[uuid.UUID]*Session),
		taskWaiters:     make(map[uuid.UUID]chan protocol.TaskResultPayload),
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
// one was evicted by Hub.Register.
func (h *Hub) UnregisterIfSame(nodeID uuid.UUID, s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.sessions[nodeID]; ok && existing == s {
		delete(h.sessions, nodeID)
		// send is already closed (by Register's eviction or WritePump exit).
		// pumpCtx will be cancelled by connectNode's defer pumpCancel().
	}
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

// GetStaging returns the active staging session for a staging agent, or nil.
func (h *Hub) GetStaging(stagingID uuid.UUID) *Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stagingSessions[stagingID]
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
	ch := make(chan protocol.TaskResultPayload, 1)
	h.taskWaitersMu.Lock()
	h.taskWaiters[taskID] = ch
	h.taskWaitersMu.Unlock()

	defer func() {
		h.taskWaitersMu.Lock()
		delete(h.taskWaiters, taskID)
		h.taskWaitersMu.Unlock()
	}()

	select {
	case result := <-ch:
		return &result, nil
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// NotifyTaskResult delivers a task result to any registered waiter.
// Called by handleTaskResult after the result is persisted.
func (h *Hub) NotifyTaskResult(taskID uuid.UUID, result protocol.TaskResultPayload) {
	h.taskWaitersMu.Lock()
	ch, ok := h.taskWaiters[taskID]
	h.taskWaitersMu.Unlock()
	if ok {
		select {
		case ch <- result:
		default:
		}
	}
}

