package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
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

// DispatchTask records a queued task in task_results, sends it to the node and
// marks it sent. A task that can't be recorded is not sent, so every dispatched
// task is tracked. ackTimeoutSecs > 0 overrides the sweeper's default ack
// timeout for long-running tasks.
func (d *Dispatcher) DispatchTask(ctx context.Context, tasks *queries.TaskResultQuerier, nodeID, taskID uuid.UUID,
	taskType protocol.TaskType, params json.RawMessage, timeoutSecs, ackTimeoutSecs int) error {
	var err error
	if ackTimeoutSecs > 0 {
		err = tasks.CreateWithAckTimeout(ctx, nodeID, taskID, string(taskType), params, ackTimeoutSecs)
	} else {
		err = tasks.Create(ctx, nodeID, taskID, string(taskType), params)
	}
	if err != nil {
		return fmt.Errorf("recording %s task: %w", taskType, err)
	}
	if err := d.Dispatch(nodeID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    taskType,
		Params:      params,
		TimeoutSecs: timeoutSecs,
	}); err != nil {
		return err
	}
	if err := tasks.SetSent(ctx, taskID); err != nil {
		slog.Warn("dispatch: SetSent failed", "node", nodeID, "task", taskID, "type", taskType, "error", err)
	}
	return nil
}
