package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditLog represents a row in the audit_logs table.
type AuditLog struct {
	ID               int64
	ActorUserID      *uuid.UUID
	ActorAgentNodeID *uuid.UUID
	Action           string
	TargetType       *string
	TargetID         *uuid.UUID
	Detail           json.RawMessage
	Outcome          *string
	CreatedAt        time.Time
}

// AuditQuerier handles audit log operations.
type AuditQuerier struct {
	pool *pgxpool.Pool
}

func NewAuditQuerier(pool *pgxpool.Pool) *AuditQuerier {
	return &AuditQuerier{pool: pool}
}

type WriteAuditParams struct {
	ActorUserID      *uuid.UUID
	ActorAgentNodeID *uuid.UUID
	Action           string
	TargetType       *string
	TargetID         *uuid.UUID
	Detail           json.RawMessage
	Outcome          string
}

func (q *AuditQuerier) Write(ctx context.Context, p WriteAuditParams) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO audit_logs
			(actor_user_id, actor_agent_node_id, action, target_type, target_id, detail, outcome)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, p.ActorUserID, p.ActorAgentNodeID, p.Action, p.TargetType, p.TargetID, p.Detail, p.Outcome)
	return err
}

type ListAuditParams struct {
	TargetID *uuid.UUID
	Limit    int
	Offset   int
}

func (q *AuditQuerier) List(ctx context.Context, p ListAuditParams) ([]AuditLog, error) {
	if p.Limit == 0 {
		p.Limit = 50
	}

	var rows interface{ Next() bool; Scan(...any) error; Close(); Err() error }
	var err error

	if p.TargetID != nil {
		rows, err = q.pool.Query(ctx, `
			SELECT id, actor_user_id, actor_agent_node_id, action, target_type, target_id,
			       detail, outcome, created_at
			FROM audit_logs
			WHERE target_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, p.TargetID, p.Limit, p.Offset)
	} else {
		rows, err = q.pool.Query(ctx, `
			SELECT id, actor_user_id, actor_agent_node_id, action, target_type, target_id,
			       detail, outcome, created_at
			FROM audit_logs
			ORDER BY created_at DESC
			LIMIT $1 OFFSET $2
		`, p.Limit, p.Offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(
			&l.ID, &l.ActorUserID, &l.ActorAgentNodeID, &l.Action,
			&l.TargetType, &l.TargetID, &l.Detail, &l.Outcome, &l.CreatedAt,
		); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
