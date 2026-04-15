package hub

import (
	"sync"

	"github.com/google/uuid"
)

// Hub maintains the set of active agent sessions and provides thread-safe
// access to them. It is the single source of truth for agent connectivity.
type Hub struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]*Session // keyed by NodeID
}

// New creates an empty Hub.
func New() *Hub {
	return &Hub{
		sessions: make(map[uuid.UUID]*Session),
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

