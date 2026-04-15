package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FailoverEvent represents a row in the failover_events table.
type FailoverEvent struct {
	ID              uuid.UUID  `json:"id"`
	ClusterID       uuid.UUID  `json:"cluster_id"`
	EventType       string     `json:"event_type"`    // "failover" | "failback" | "maintenance_failover"
	Trigger         string     `json:"trigger"`       // "disconnect" | "heartbeat" | "maintenance"
	FailedNodeID    *uuid.UUID `json:"failed_node_id,omitempty"`
	FailedNodeName  string     `json:"failed_node_name"`
	TargetNodeID    *uuid.UUID `json:"target_node_id,omitempty"`
	TargetNodeName  string     `json:"target_node_name"`
	Success         bool       `json:"success"`
	Reason          string     `json:"reason,omitempty"`
	OccurredAt      time.Time  `json:"occurred_at"`
}

// CreateFailoverEventParams holds the fields for inserting a new failover event.
type CreateFailoverEventParams struct {
	ClusterID      uuid.UUID
	EventType      string
	Trigger        string
	FailedNodeID   *uuid.UUID
	FailedNodeName string
	TargetNodeID   *uuid.UUID
	TargetNodeName string
	Success        bool
	Reason         string
}

// FailoverEventQuerier performs failover_events table operations.
type FailoverEventQuerier struct {
	pool *pgxpool.Pool
}

func NewFailoverEventQuerier(pool *pgxpool.Pool) *FailoverEventQuerier {
	return &FailoverEventQuerier{pool: pool}
}

// Create inserts a new failover event record.
func (q *FailoverEventQuerier) Create(ctx context.Context, p CreateFailoverEventParams) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO failover_events
			(cluster_id, event_type, trigger,
			 failed_node_id, failed_node_name,
			 target_node_id, target_node_name,
			 success, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		p.ClusterID, p.EventType, p.Trigger,
		p.FailedNodeID, p.FailedNodeName,
		p.TargetNodeID, p.TargetNodeName,
		p.Success, p.Reason,
	)
	return err
}

// ListByCluster returns the most recent failover events for a cluster,
// newest first. Limit caps the result set (max 200).
func (q *FailoverEventQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID, limit int) ([]FailoverEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, event_type, trigger,
		       failed_node_id, failed_node_name,
		       target_node_id, target_node_name,
		       success, reason, occurred_at
		FROM failover_events
		WHERE cluster_id = $1
		ORDER BY occurred_at DESC
		LIMIT $2`,
		clusterID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []FailoverEvent
	for rows.Next() {
		var e FailoverEvent
		if err := rows.Scan(
			&e.ID, &e.ClusterID, &e.EventType, &e.Trigger,
			&e.FailedNodeID, &e.FailedNodeName,
			&e.TargetNodeID, &e.TargetNodeName,
			&e.Success, &e.Reason, &e.OccurredAt,
		); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
