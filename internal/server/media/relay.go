// Package media manages in-memory media file relay between agents.
// The server never writes chunks to disk; it pipes them from source to target.
package media

import (
	"fmt"
	"io"
	"sync"

	"github.com/google/uuid"
)

// Transfer tracks an active media sync relay.
type Transfer struct {
	ID         uuid.UUID
	SourceNode uuid.UUID
	TargetNode uuid.UUID

	// pipe connects the source agent's write side to the target agent's read side.
	pr *io.PipeReader
	pw *io.PipeWriter

	mu     sync.Mutex
	done   bool
	errVal error
}

// Manager holds all active transfers.
type Manager struct {
	mu        sync.Mutex
	transfers map[uuid.UUID]*Transfer
}

func NewManager() *Manager {
	return &Manager{transfers: make(map[uuid.UUID]*Transfer)}
}

// Create registers a new transfer and returns it.
func (m *Manager) Create(sourceNode, targetNode uuid.UUID) *Transfer {
	pr, pw := io.Pipe()
	t := &Transfer{
		ID:         uuid.New(),
		SourceNode: sourceNode,
		TargetNode: targetNode,
		pr:         pr,
		pw:         pw,
	}
	m.mu.Lock()
	m.transfers[t.ID] = t
	m.mu.Unlock()
	return t
}

// Get returns a transfer by ID.
func (m *Manager) Get(id uuid.UUID) (*Transfer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.transfers[id]
	return t, ok
}

// Remove deletes a transfer and closes its pipe.
func (m *Manager) Remove(id uuid.UUID) {
	m.mu.Lock()
	t, ok := m.transfers[id]
	if ok {
		delete(m.transfers, id)
	}
	m.mu.Unlock()
	if ok {
		t.close(nil)
	}
}

// WriteChunk writes a chunk from the source agent into the pipe.
// Called by the source agent's inbound handler for each media.chunk message.
func (t *Transfer) WriteChunk(data []byte, eof bool) error {
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return fmt.Errorf("transfer already complete")
	}
	t.mu.Unlock()

	if _, err := t.pw.Write(data); err != nil {
		return err
	}
	if eof {
		t.close(nil)
	}
	return nil
}

// Reader returns the read side of the pipe (consumed by the target agent relay).
func (t *Transfer) Reader() io.Reader {
	return t.pr
}

func (t *Transfer) close(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	t.errVal = err
	if err != nil {
		t.pw.CloseWithError(err)
	} else {
		t.pw.Close()
	}
}
