package hub

import (
	"testing"

	"github.com/google/uuid"
)

func lost(w *taskWatch) bool {
	select {
	case <-w.lost:
		return true
	default:
		return false
	}
}

// A heartbeat without the task means lost only once the agent has shown it
// holds the task (ack or an earlier heartbeat), and only for that node.
func TestTaskWatchLost(t *testing.T) {
	h := New()
	node, other := uuid.New(), uuid.New()

	id := uuid.New()
	w, done := h.watchTask(id, node)
	defer done()
	h.NotifyHeartbeat(node, nil) // sent before the agent had the task
	if lost(w) {
		t.Fatal("lost before the agent showed it had the task")
	}
	h.NotifyHeartbeat(node, []string{id.String()})
	h.NotifyHeartbeat(other, nil)
	if lost(w) {
		t.Fatal("another node's heartbeat counted")
	}
	h.NotifyHeartbeat(node, nil)
	if !lost(w) {
		t.Fatal("not lost after a heartbeat without the task")
	}
	h.NotifyHeartbeat(node, nil) // closing twice must not panic

	id2 := uuid.New()
	w2, done2 := h.watchTask(id2, node)
	defer done2()
	h.NotifyTaskAck(id2)
	h.NotifyHeartbeat(node, []string{"something else"})
	if !lost(w2) {
		t.Fatal("not lost after its ack and a heartbeat without it")
	}
}
