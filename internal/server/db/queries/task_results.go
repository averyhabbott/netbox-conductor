package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TaskResult tracks the lifecycle of a dispatched task.
type TaskResult struct {
	ID              uuid.UUID
	NodeID          uuid.UUID
	TaskID          uuid.UUID
	TaskType        string
	Status          string          // queued → sent → ack → success|failure|timeout
	RequestPayload  json.RawMessage
	ResponsePayload json.RawMessage
	QueuedAt        time.Time
	CompletedAt     *time.Time
}

type TaskResultQuerier struct {
	pool *pgxpool.Pool
}

func NewTaskResultQuerier(pool *pgxpool.Pool) *TaskResultQuerier {
	return &TaskResultQuerier{pool: pool}
}

// Create records a newly queued task.
func (q *TaskResultQuerier) Create(ctx context.Context, nodeID, taskID uuid.UUID, taskType string, payload json.RawMessage) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO task_results (node_id, task_id, task_type, status, request_payload)
		VALUES ($1, $2, $3, 'queued', $4)`,
		nodeID, taskID, taskType, payload)
	return err
}

// SetSent marks the task as sent (dispatch confirmed to agent session).
func (q *TaskResultQuerier) SetSent(ctx context.Context, taskID uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE task_results SET status = 'sent' WHERE task_id = $1`, taskID)
	return err
}

// SetAck marks the task as acknowledged by the agent.
func (q *TaskResultQuerier) SetAck(ctx context.Context, taskID uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE task_results SET status = 'ack' WHERE task_id = $1`, taskID)
	return err
}

// Complete records the final outcome of a task.
func (q *TaskResultQuerier) Complete(ctx context.Context, taskID uuid.UUID, success bool, responsePayload json.RawMessage) error {
	status := "success"
	if !success {
		status = "failure"
	}
	_, err := q.pool.Exec(ctx, `
		UPDATE task_results
		SET status = $2, response_payload = $3, completed_at = now()
		WHERE task_id = $1`,
		taskID, status, responsePayload)
	return err
}

// GetByTaskID returns a single task result.
func (q *TaskResultQuerier) GetByTaskID(ctx context.Context, taskID uuid.UUID) (*TaskResult, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, node_id, task_id, task_type, status,
		       request_payload, response_payload, queued_at, completed_at
		FROM task_results WHERE task_id = $1`, taskID)

	var t TaskResult
	err := row.Scan(&t.ID, &t.NodeID, &t.TaskID, &t.TaskType, &t.Status,
		&t.RequestPayload, &t.ResponsePayload, &t.QueuedAt, &t.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListByNode returns recent task results for a node.
func (q *TaskResultQuerier) ListByNode(ctx context.Context, nodeID uuid.UUID, limit int) ([]TaskResult, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, node_id, task_id, task_type, status,
		       request_payload, response_payload, queued_at, completed_at
		FROM task_results
		WHERE node_id = $1
		ORDER BY queued_at DESC
		LIMIT $2`,
		nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TaskResult
	for rows.Next() {
		var t TaskResult
		if err := rows.Scan(&t.ID, &t.NodeID, &t.TaskID, &t.TaskType, &t.Status,
			&t.RequestPayload, &t.ResponsePayload, &t.QueuedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

// TimeoutStale marks tasks that have been stuck in "sent", "ack", or "queued"
// status longer than the given durations as "timeout". Returns the number of rows affected.
func (q *TaskResultQuerier) TimeoutStale(ctx context.Context, sentTimeout, ackTimeout, queuedTimeout time.Duration) (int64, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE task_results
		SET status = 'timeout', completed_at = now()
		WHERE (status = 'sent'   AND queued_at < now() - $1::interval)
		   OR (status = 'ack'    AND queued_at < now() - $2::interval)
		   OR (status = 'queued' AND queued_at < now() - $3::interval)`,
		sentTimeout.String(), ackTimeout.String(), queuedTimeout.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListPendingByNode returns tasks in "queued" or "sent" state for a node.
// "sent" tasks were dispatched before a disconnect and need to be re-sent.
// "queued" tasks were created while the node was offline (e.g. a stop.netbox
// queued to prevent split-brain) and need to be dispatched for the first time.
func (q *TaskResultQuerier) ListPendingByNode(ctx context.Context, nodeID uuid.UUID) ([]TaskResult, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, node_id, task_id, task_type, status,
		       request_payload, response_payload, queued_at, completed_at
		FROM task_results
		WHERE node_id = $1 AND status IN ('queued', 'sent')
		ORDER BY queued_at`,
		nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TaskResult
	for rows.Next() {
		var t TaskResult
		if err := rows.Scan(&t.ID, &t.NodeID, &t.TaskID, &t.TaskType, &t.Status,
			&t.RequestPayload, &t.ResponsePayload, &t.QueuedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

// ListByNodeIDs returns recent tasks for a set of nodes, ordered newest first.
// Used for cluster-level history views (e.g. Patroni history across all nodes).
func (q *TaskResultQuerier) ListByNodeIDs(ctx context.Context, nodeIDs []uuid.UUID, limit int) ([]TaskResult, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	rows, err := q.pool.Query(ctx, `
		SELECT id, node_id, task_id, task_type, status,
		       request_payload, response_payload, queued_at, completed_at
		FROM task_results
		WHERE node_id = ANY($1)
		ORDER BY queued_at DESC
		LIMIT $2`,
		nodeIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TaskResult
	for rows.Next() {
		var t TaskResult
		if err := rows.Scan(&t.ID, &t.NodeID, &t.TaskID, &t.TaskType, &t.Status,
			&t.RequestPayload, &t.ResponsePayload, &t.QueuedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

// ListByConfigPush returns all task results for a given set of task IDs (e.g. a push batch).
func (q *TaskResultQuerier) ListByTaskIDs(ctx context.Context, taskIDs []uuid.UUID) ([]TaskResult, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	// Build $1,$2,... parameter list
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		args[i] = id
	}

	rows, err := q.pool.Query(ctx, `
		SELECT id, node_id, task_id, task_type, status,
		       request_payload, response_payload, queued_at, completed_at
		FROM task_results
		WHERE task_id = ANY($1)
		ORDER BY queued_at`,
		taskIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TaskResult
	for rows.Next() {
		var t TaskResult
		if err := rows.Scan(&t.ID, &t.NodeID, &t.TaskID, &t.TaskType, &t.Status,
			&t.RequestPayload, &t.ResponsePayload, &t.QueuedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}
