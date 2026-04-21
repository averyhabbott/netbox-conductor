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
	ID               int64           `json:"id"`
	ActorUserID      *uuid.UUID      `json:"actor_user_id"`
	ActorUsername    *string         `json:"actor_username"`
	ActorAgentNodeID *uuid.UUID      `json:"actor_agent_node_id"`
	ActorNodeName    *string         `json:"actor_node_name"`
	Action           string          `json:"action"`
	TargetType       *string         `json:"target_type"`
	TargetID         *uuid.UUID      `json:"target_id"`
	TargetName       *string         `json:"target_name"`
	Detail           json.RawMessage `json:"detail"`
	Outcome          *string         `json:"outcome"`
	CreatedAt        time.Time       `json:"created_at"`
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

// ListByCluster returns audit logs scoped to a cluster: entries targeting the cluster
// itself or any node that belongs to it.
func (q *AuditQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID, limit int) ([]AuditLog, error) {
	if limit == 0 {
		limit = 200
	}
	rows, err := q.pool.Query(ctx, `
		SELECT al.id, al.actor_user_id, u.username, al.actor_agent_node_id, an.hostname,
		       al.action, al.target_type, al.target_id,
		       COALESCE(tc.name, tn.hostname, tu.username),
		       al.detail, al.outcome, al.created_at
		FROM audit_logs al
		LEFT JOIN users u  ON u.id  = al.actor_user_id
		LEFT JOIN nodes an ON an.id = al.actor_agent_node_id
		LEFT JOIN clusters tc ON tc.id = al.target_id AND al.target_type = 'cluster'
		LEFT JOIN nodes    tn ON tn.id = al.target_id AND al.target_type = 'node'
		LEFT JOIN users    tu ON tu.id = al.target_id AND al.target_type = 'user'
		WHERE al.target_id = $1
		   OR al.target_id IN (SELECT id FROM nodes WHERE cluster_id = $1)
		ORDER BY al.created_at DESC
		LIMIT $2
	`, clusterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(
			&l.ID, &l.ActorUserID, &l.ActorUsername, &l.ActorAgentNodeID, &l.ActorNodeName,
			&l.Action, &l.TargetType, &l.TargetID, &l.TargetName,
			&l.Detail, &l.Outcome, &l.CreatedAt,
		); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func (q *AuditQuerier) List(ctx context.Context, p ListAuditParams) ([]AuditLog, error) {
	if p.Limit == 0 {
		p.Limit = 50
	}

	var rows interface{ Next() bool; Scan(...any) error; Close(); Err() error }
	var err error

	const auditSelect = `
		SELECT al.id, al.actor_user_id, u.username, al.actor_agent_node_id, an.hostname,
		       al.action, al.target_type, al.target_id,
		       COALESCE(tc.name, tn.hostname, tu.username),
		       al.detail, al.outcome, al.created_at
		FROM audit_logs al
		LEFT JOIN users u  ON u.id  = al.actor_user_id
		LEFT JOIN nodes an ON an.id = al.actor_agent_node_id
		LEFT JOIN clusters tc ON tc.id = al.target_id AND al.target_type = 'cluster'
		LEFT JOIN nodes    tn ON tn.id = al.target_id AND al.target_type = 'node'
		LEFT JOIN users    tu ON tu.id = al.target_id AND al.target_type = 'user'`

	if p.TargetID != nil {
		rows, err = q.pool.Query(ctx, auditSelect+`
			WHERE al.target_id = $1
			ORDER BY al.created_at DESC
			LIMIT $2 OFFSET $3
		`, p.TargetID, p.Limit, p.Offset)
	} else {
		rows, err = q.pool.Query(ctx, auditSelect+`
			ORDER BY al.created_at DESC
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
			&l.ID, &l.ActorUserID, &l.ActorUsername, &l.ActorAgentNodeID, &l.ActorNodeName,
			&l.Action, &l.TargetType, &l.TargetID, &l.TargetName,
			&l.Detail, &l.Outcome, &l.CreatedAt,
		); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
