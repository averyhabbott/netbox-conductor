package oplock

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestTryLock(t *testing.T) {
	l := New()
	a, b := uuid.New(), uuid.New()

	unlock, err := l.TryLock(a, "Configure Failover")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	var busy *BusyError
	if _, err := l.TryLock(a, "a restore"); !errors.As(err, &busy) || busy.Running != "Configure Failover" {
		t.Fatalf("second lock on same cluster: got %v, want BusyError naming Configure Failover", err)
	}
	if _, err := l.TryLock(b, "a restore"); err != nil {
		t.Fatalf("other cluster should be free: %v", err)
	}

	unlock()
	unlock() // second call must not release someone else's lock
	again, err := l.TryLock(a, "a restore")
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock()
	if _, err := l.TryLock(a, "a backup"); err == nil {
		t.Fatal("stale unlock released a newer holder's lock")
	}
	again()
}
