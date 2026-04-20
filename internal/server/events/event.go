package events

import (
	"time"

	"github.com/google/uuid"
)

// Category constants for the events.category column.
const (
	CategoryCluster = "cluster"
	CategoryService = "service"
	CategoryHA      = "ha"
	CategoryConfig  = "config"
	CategoryAgent   = "agent"
)

// Severity constants for the events.severity column.
const (
	SeverityDebug    = "debug"
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// ActorSystem is the actor label used when an event is triggered automatically
// by the conductor or agent rather than by a human user.
const ActorSystem = "system"

// SeverityOrder maps severity names to their numeric rank for comparison.
var SeverityOrder = map[string]int{
	SeverityDebug:    0,
	SeverityInfo:     1,
	SeverityWarn:     2,
	SeverityError:    3,
	SeverityCritical: 4,
}

// Event is the canonical event type used throughout the server.
// It maps 1:1 with a row in the events table.
// ClusterName and NodeName are not persisted; they are populated by the
// emitter's NameResolver before events reach transport sinks.
type Event struct {
	ID          uuid.UUID              `json:"id"`
	ClusterID   *uuid.UUID             `json:"cluster_id,omitempty"`
	NodeID      *uuid.UUID             `json:"node_id,omitempty"`
	ClusterName *string                `json:"cluster_name,omitempty"`
	NodeName    *string                `json:"node_name,omitempty"`
	Category    string                 `json:"category"`
	Severity    string                 `json:"severity"`
	Code        string                 `json:"code"`
	Message     string                 `json:"message"`
	Actor       string                 `json:"actor"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	OccurredAt  time.Time              `json:"occurred_at"`
}

// Emitter is implemented by any type that can receive and persist events.
// Handlers and the failover manager accept this interface so they are not
// coupled to the concrete emitter implementation.
type Emitter interface {
	Emit(e Event)
}

// Builder provides a fluent API for constructing events.
type Builder struct {
	e Event
}

// New starts building an event with required fields.
func New(category, severity, code, message, actor string) *Builder {
	return &Builder{e: Event{
		ID:         uuid.New(),
		Category:   category,
		Severity:   severity,
		Code:       code,
		Message:    message,
		Actor:      actor,
		OccurredAt: time.Now().UTC(),
	}}
}

func (b *Builder) Cluster(id uuid.UUID) *Builder     { b.e.ClusterID = &id; return b }
func (b *Builder) Node(id uuid.UUID) *Builder        { b.e.NodeID = &id; return b }
func (b *Builder) ClusterName(n string) *Builder     { b.e.ClusterName = &n; return b }
func (b *Builder) NodeName(n string) *Builder        { b.e.NodeName = &n; return b }
func (b *Builder) Meta(k string, v interface{}) *Builder {
	if b.e.Metadata == nil {
		b.e.Metadata = make(map[string]interface{})
	}
	b.e.Metadata[k] = v
	return b
}
func (b *Builder) Build() Event { return b.e }
