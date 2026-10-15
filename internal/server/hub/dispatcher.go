package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// ErrNoResult: a tracked task ended without its result reaching the conductor
// (never sent, lost, or the agent stayed away past the task's limit).
var ErrNoResult = errors.New("no result from the agent")

// liveCheckInterval is how often a wait past its task's limit checks the agent
// is still connected.
const liveCheckInterval = 5 * time.Second

// Task is a dispatched task followed until its outcome is known; see Wait.
type Task struct {
	ID       uuid.UUID
	d        *Dispatcher
	tasks    *queries.TaskResultQuerier
	node     uuid.UUID
	deadline time.Time
	w        *taskWatch
	done     func()
}

// StartTask dispatches a task to node and follows it from the agent's acks and
// heartbeats; call Wait for its outcome. timeoutSecs is the agent-side limit.
func (d *Dispatcher) StartTask(ctx context.Context, tasks *queries.TaskResultQuerier, node uuid.UUID,
	taskType protocol.TaskType, params json.RawMessage, timeoutSecs int) (*Task, error) {
	t := &Task{ID: uuid.New(), d: d, tasks: tasks, node: node, deadline: time.Now().Add(time.Duration(timeoutSecs) * time.Second)}
	t.w, t.done = d.hub.watchTask(t.ID, node)
	if err := d.DispatchTask(ctx, tasks, node, t.ID, taskType, params, timeoutSecs, timeoutSecs); err != nil {
		t.done()
		return nil, err
	}
	return t, nil
}

// Wait returns the task's output, with an error for a failed task or ErrNoResult.
// The task is over when its result arrives; when the agent, connected, stops
// listing it in heartbeats; or when its limit has passed and the agent is away
// or never showed it had the task. The agent enforces the limit, so a task it
// lists past the limit is only queued behind others, and is waited for.
func (t *Task) Wait(ctx context.Context) (string, error) {
	defer t.done()
	tick := time.NewTicker(liveCheckInterval)
	defer tick.Stop()
	for {
		select {
		case res := <-t.w.result:
			return outcome(res)
		case <-t.w.lost:
			select {
			case res := <-t.w.result:
				return outcome(res)
			default:
			}
			return "", t.orphan("the agent no longer has the task and its result never arrived")
		case <-tick.C:
			if time.Now().After(t.deadline) && (t.d.hub.Get(t.node) == nil || !t.w.known.Load()) {
				return "", t.orphan("timed out: the agent was away or never received the task")
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func outcome(res protocol.TaskResultPayload) (string, error) {
	if !res.Success {
		return res.Output, errors.New(res.ErrorMsg)
	}
	return res.Output, nil
}

// orphan records the task as timed out and returns ErrNoResult with reason.
func (t *Task) orphan(reason string) error {
	if err := t.tasks.MarkOrphaned(context.Background(), t.ID, reason); err != nil {
		slog.Warn("dispatch: marking task orphaned", "task", t.ID, "error", err)
	}
	return fmt.Errorf("%w: %s", ErrNoResult, reason)
}

// RunTask dispatches a task to node and waits for its outcome (see Wait).
// A dispatch failure is returned as ErrNoResult.
func (d *Dispatcher) RunTask(ctx context.Context, tasks *queries.TaskResultQuerier, node uuid.UUID,
	taskType protocol.TaskType, params json.RawMessage, timeoutSecs int) (string, error) {
	t, err := d.StartTask(ctx, tasks, node, taskType, params, timeoutSecs)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoResult, err)
	}
	return t.Wait(ctx)
}
