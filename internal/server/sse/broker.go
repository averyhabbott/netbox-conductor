// Package sse provides a simple Server-Sent Events broker.
// The frontend subscribes to GET /api/v1/events and receives real-time
// updates whenever agent status, heartbeat, or task outcomes change.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// EventType identifies the kind of SSE event.
type EventType string

const (
	EventNodeStatus   EventType = "node.status"    // agent connected/disconnected
	EventNodeHeartbeat EventType = "node.heartbeat" // heartbeat metrics
	EventTaskComplete EventType = "task.complete"   // task result received
	EventPatroniState EventType = "patroni.state"   // patroni role change
)

// Event is a single SSE message.
type Event struct {
	Type    EventType `json:"type"`
	NodeID  uuid.UUID `json:"node_id,omitempty"`
	Payload any       `json:"payload"`
}

// Broker fans out SSE events to all subscribed HTTP clients.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]chan Event // key = subscriber ID
}

// New creates a Broker.
func New() *Broker {
	return &Broker{
		subscribers: make(map[string]chan Event),
	}
}

// Publish sends an event to all current subscribers.
// Non-blocking: subscribers with full buffers are skipped.
func (b *Broker) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// subscribe adds a new subscriber and returns its channel and an unsubscribe func.
func (b *Broker) subscribe() (string, <-chan Event, func()) {
	id := uuid.New().String()
	ch := make(chan Event, 64)

	b.mu.Lock()
	b.subscribers[id] = ch
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		close(ch)
		b.mu.Unlock()
	}
	return id, ch, unsub
}

// ServeHTTP implements http.Handler so the broker can be mounted directly.
// Clients receive a stream of JSON-encoded events.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	_, events, unsub := b.subscribe()
	defer unsub()

	// Send a heartbeat comment every 30s to keep the connection alive through proxies.
	// The actual data events come from Publish().
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}
