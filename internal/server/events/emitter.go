package events

import (
	"context"
	"log/slog"
)

// Sink receives events for further processing (syslog, alerting, SSE).
// Each registered sink is called synchronously inside Emit.
type Sink interface {
	OnEvent(e Event)
}

// Store persists events to the database.
type Store interface {
	Insert(ctx context.Context, e Event) error
}

// DefaultEmitter writes events to the database and fans them out to
// any number of registered sinks (alert engine, syslog forwarder, SSE broker).
type DefaultEmitter struct {
	store Store
	sinks []Sink
}

// NewEmitter creates an emitter backed by store and zero or more sinks.
// Additional sinks can be added via RegisterSink.
func NewEmitter(store Store, sinks ...Sink) *DefaultEmitter {
	return &DefaultEmitter{store: store, sinks: sinks}
}

// RegisterSink adds a sink that receives every event after DB insertion.
func (e *DefaultEmitter) RegisterSink(s Sink) {
	e.sinks = append(e.sinks, s)
}

// Emit persists the event and notifies all sinks.
// It is safe to call from goroutines.  DB errors are logged but never panic.
func (e *DefaultEmitter) Emit(ev Event) {
	if err := e.store.Insert(context.Background(), ev); err != nil {
		slog.Warn("events: failed to persist event",
			"code", ev.Code, "message", ev.Message, "error", err)
	} else {
		slog.Debug("events: persisted", "code", ev.Code, "category", ev.Category, "message", ev.Message)
	}
	for _, s := range e.sinks {
		s.OnEvent(ev)
	}
}

// NoopEmitter discards all events.  Useful in tests and during startup
// before the real emitter is wired up.
type NoopEmitter struct{}

func (NoopEmitter) Emit(Event) {}
