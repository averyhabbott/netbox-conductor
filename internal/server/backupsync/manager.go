package backupsync

import (
	"sync"

	"github.com/google/uuid"
)

// Manager tracks active backup sync transfers: transferID → target node IDs.
// The source agent sends backup.chunk messages; the conductor fans them out to
// all registered targets.
type Manager struct {
	mu        sync.RWMutex
	transfers map[uuid.UUID][]uuid.UUID
}

func New() *Manager {
	return &Manager{transfers: make(map[uuid.UUID][]uuid.UUID)}
}

func (m *Manager) Register(transferID uuid.UUID, targets []uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transfers[transferID] = targets
}

func (m *Manager) GetTargets(transferID uuid.UUID) ([]uuid.UUID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.transfers[transferID]
	return t, ok
}

// DropTarget stops relaying a transfer to one node (it went offline or fell behind).
func (m *Manager) DropTarget(transferID, nodeID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.transfers[transferID]
	for i, id := range t {
		if id == nodeID {
			m.transfers[transferID] = append(t[:i:i], t[i+1:]...)
			return
		}
	}
}

func (m *Manager) Remove(transferID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.transfers, transferID)
}
