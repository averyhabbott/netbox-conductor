package hub

import (
	"sync"

	"github.com/google/uuid"
)

// Hub maintains the set of active agent sessions and provides thread-safe
// access to them. It is the single source of truth for agent connectivity.
type Hub struct {
	mu              sync.RWMutex
	sessions        map[uuid.UUID]*Session // keyed by NodeID (assigned agents)
	stagingSessions map[uuid.UUID]*Session // keyed by StagingAgentID (unassigned agents)
}

// New creates an empty Hub.
func New() *Hub {
	return &Hub{
		sessions:        make(map[uuid.UUID]*Session),
		stagingSessions: make(map[uuid.UUID]*Session),
	}
}

// Register adds a session. If a session for the same NodeID already exists
// (stale connection), it is evicted first.
func (h *Hub) Register(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.sessions[s.NodeID]; ok {
		close(old.send) // signal the old write pump to exit
	}
	h.sessions[s.NodeID] = s
}

// Unregister removes a session by NodeID.
func (h *Hub) Unregister(nodeID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[nodeID]; ok {
		close(s.send)
		delete(h.sessions, nodeID)
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

