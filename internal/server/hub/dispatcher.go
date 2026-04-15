package hub

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// Dispatcher wraps the Hub and provides typed task-dispatch methods.
type Dispatcher struct {
	hub *Hub
}

// NewDispatcher creates a Dispatcher backed by the given Hub.
func NewDispatcher(h *Hub) *Dispatcher {
	return &Dispatcher{hub: h}
}

// SendRaw delivers a pre-built envelope to a node.
// Returns an error if the node is not connected or the buffer is full.
func (d *Dispatcher) SendRaw(nodeID uuid.UUID, env protocol.Envelope) error {
	s := d.hub.Get(nodeID)
	if s == nil {
		return fmt.Errorf("node %s is not connected", nodeID)
	}
	if !s.Send(env) {
		return fmt.Errorf("send buffer full for node %s", nodeID)
	}
	return nil
}

// Dispatch sends a TaskDispatch message to a node.
func (d *Dispatcher) Dispatch(nodeID uuid.UUID, task protocol.TaskDispatchPayload) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshaling task dispatch: %w", err)
	}
	env := protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeTaskDispatch,
		Payload: json.RawMessage(payload),
	}
	return d.SendRaw(nodeID, env)
}

// ServerHello sends a server.hello acknowledgement to a node.
func (d *Dispatcher) ServerHello(nodeID uuid.UUID, accepted bool, reason, version string) error {
	payload, err := json.Marshal(protocol.ServerHelloPayload{
		Accepted:      accepted,
		RejectReason:  reason,
		ServerVersion: version,
	})
	if err != nil {
		return err
	}
	return d.SendRaw(nodeID, protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeServerHello,
		Payload: json.RawMessage(payload),
	})
}
