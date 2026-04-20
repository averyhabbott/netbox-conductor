package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertFireLog is one row in the alert_fire_log history table.
type AlertFireLog struct {
	ID            uuid.UUID  `json:"id"`
	RuleID        *uuid.UUID `json:"rule_id,omitempty"`
	RuleName      string     `json:"rule_name"`
	TransportID   *uuid.UUID `json:"transport_id,omitempty"`
	TransportName string     `json:"transport_name"`
	TransportType string     `json:"transport_type"`
	ClusterID     *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName   *string    `json:"cluster_name,omitempty"`
	NodeID        *uuid.UUID `json:"node_id,omitempty"`
	NodeName      *string    `json:"node_name,omitempty"`
	EventCode     string     `json:"event_code"`
	EventMessage  string     `json:"event_message"`
	EventSeverity string     `json:"event_severity"`
	IsResolve     bool       `json:"is_resolve"`
	FiredAt       time.Time  `json:"fired_at"`
}

// AlertFireLogEntry is the input to Insert.
type AlertFireLogEntry struct {
	RuleID        *uuid.UUID
	RuleName      string
	TransportID   *uuid.UUID
	TransportName string
	TransportType string
	ClusterID     *uuid.UUID
	ClusterName   *string
	NodeID        *uuid.UUID
	NodeName      *string
	EventCode     string
	EventMessage  string
	EventSeverity string
	IsResolve     bool
}

// AlertFireLogQuerier performs alert_fire_log operations.
type AlertFireLogQuerier struct {
	pool *pgxpool.Pool
}

func NewAlertFireLogQuerier(pool *pgxpool.Pool) *AlertFireLogQuerier {
	return &AlertFireLogQuerier{pool: pool}
}

// Insert records one alert delivery.
func (q *AlertFireLogQuerier) Insert(ctx context.Context, e AlertFireLogEntry) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO alert_fire_log
			(rule_id, rule_name, transport_id, transport_name, transport_type,
			 cluster_id, cluster_name, node_id, node_name,
			 event_code, event_message, event_severity, is_resolve)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.RuleID, e.RuleName, e.TransportID, e.TransportName, e.TransportType,
		e.ClusterID, e.ClusterName, e.NodeID, e.NodeName,
		e.EventCode, e.EventMessage, e.EventSeverity, e.IsResolve,
	)
	return err
}

// List returns up to 500 fire log entries from the last 30 days, newest first.
func (q *AlertFireLogQuerier) List(ctx context.Context) ([]AlertFireLog, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, rule_id, rule_name, transport_id, transport_name, transport_type,
		       cluster_id, cluster_name, node_id, node_name,
		       event_code, event_message, event_severity, is_resolve, fired_at
		FROM alert_fire_log
		WHERE fired_at > now() - interval '30 days'
		ORDER BY fired_at DESC
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AlertFireLog, 0)
	for rows.Next() {
		var e AlertFireLog
		if err := rows.Scan(
			&e.ID, &e.RuleID, &e.RuleName, &e.TransportID, &e.TransportName, &e.TransportType,
			&e.ClusterID, &e.ClusterName, &e.NodeID, &e.NodeName,
			&e.EventCode, &e.EventMessage, &e.EventSeverity, &e.IsResolve, &e.FiredAt,
		); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
