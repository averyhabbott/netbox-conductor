// Package oplock lets one disruptive operation run on a cluster at a time
// (Configure Failover, restore, backup, Patroni config deploy, …). A second
// request while one is running is refused rather than queued.
//
// Locks live in memory: the background work they guard doesn't survive a
// conductor restart either, so nothing is left to protect after one.
package oplock

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// BusyError is returned by TryLock when another operation holds the cluster.
type BusyError struct {
	Running string // the operation holding the cluster
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("cluster is busy: %s is in progress", e.Running)
}

// Locks tracks which operation, if any, holds each cluster.
type Locks struct {
	mu      sync.Mutex
	running map[uuid.UUID]string
}

// New creates an empty set of cluster locks.
func New() *Locks {
	return &Locks{running: make(map[uuid.UUID]string)}
}

// TryLock claims clusterID for op, or returns a *BusyError naming the
// operation already holding it. The returned unlock is safe to call more than
// once, so a handler can defer it and also hand it to background work.
func (l *Locks) TryLock(clusterID uuid.UUID, op string) (unlock func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if running, busy := l.running[clusterID]; busy {
		return nil, &BusyError{Running: running}
	}
	l.running[clusterID] = op
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.running, clusterID)
			l.mu.Unlock()
		})
	}, nil
}
