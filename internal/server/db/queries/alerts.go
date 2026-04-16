package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertConfig is a named destination for alert notifications.
type AlertConfig struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`       // webhook | email
	Enabled    bool      `json:"enabled"`
	Conditions []string  `json:"conditions"` // agent_disconnected | netbox_down | rq_down
	WebhookURL *string   `json:"webhook_url,omitempty"`
	EmailTo    *string   `json:"email_to,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ActiveAlert represents an alert that has fired and may not yet be resolved.
type ActiveAlert struct {
	ID             uuid.UUID  `json:"id"`
	ClusterID      *uuid.UUID `json:"cluster_id,omitempty"`
	NodeID         *uuid.UUID `json:"node_id,omitempty"`
	Severity       string     `json:"severity"`
	Condition      string     `json:"condition"`
	Message        string     `json:"message"`
	FiredAt        time.Time  `json:"fired_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

// UpsertAlertConfigParams holds the fields for creating or updating an alert config.
type UpsertAlertConfigParams struct {
	Name       string
	Type       string
	Enabled    bool
	Conditions []string
	WebhookURL *string
	EmailTo    *string
}

// AlertQuerier performs alert_configs and active_alerts operations.
type AlertQuerier struct {
	pool *pgxpool.Pool
}

func NewAlertQuerier(pool *pgxpool.Pool) *AlertQuerier {
	return &AlertQuerier{pool: pool}
}

// ─── Alert Configs ────────────────────────────────────────────────────────────

func (q *AlertQuerier) CreateConfig(ctx context.Context, p UpsertAlertConfigParams) (*AlertConfig, error) {
	condJSON, _ := json.Marshal(p.Conditions)
	var a AlertConfig
	row := q.pool.QueryRow(ctx, `
		INSERT INTO alert_configs (name, type, enabled, conditions, webhook_url, email_to)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, type, enabled, conditions, webhook_url, email_to, created_at, updated_at`,
		p.Name, p.Type, p.Enabled, condJSON, p.WebhookURL, p.EmailTo,
	)
	if err := scanAlertConfig(row, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (q *AlertQuerier) ListConfigs(ctx context.Context) ([]AlertConfig, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, type, enabled, conditions, webhook_url, email_to, created_at, updated_at
		FROM alert_configs ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var configs []AlertConfig
	for rows.Next() {
		var a AlertConfig
		if err := scanAlertConfigRow(rows, &a); err != nil {
			return nil, err
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

func (q *AlertQuerier) GetConfig(ctx context.Context, id uuid.UUID) (*AlertConfig, error) {
	var a AlertConfig
	row := q.pool.QueryRow(ctx, `
		SELECT id, name, type, enabled, conditions, webhook_url, email_to, created_at, updated_at
		FROM alert_configs WHERE id = $1`, id)
	if err := scanAlertConfig(row, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (q *AlertQuerier) UpdateConfig(ctx context.Context, id uuid.UUID, p UpsertAlertConfigParams) (*AlertConfig, error) {
	condJSON, _ := json.Marshal(p.Conditions)
	var a AlertConfig
	row := q.pool.QueryRow(ctx, `
		UPDATE alert_configs
		SET name = $2, type = $3, enabled = $4, conditions = $5,
		    webhook_url = $6, email_to = $7, updated_at = now()
		WHERE id = $1
		RETURNING id, name, type, enabled, conditions, webhook_url, email_to, created_at, updated_at`,
		id, p.Name, p.Type, p.Enabled, condJSON, p.WebhookURL, p.EmailTo,
	)
	if err := scanAlertConfig(row, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (q *AlertQuerier) DeleteConfig(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM alert_configs WHERE id = $1`, id)
	return err
}

// EnabledConfigsForCondition returns all enabled configs that subscribe to the given condition.
func (q *AlertQuerier) EnabledConfigsForCondition(ctx context.Context, condition string) ([]AlertConfig, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, type, enabled, conditions, webhook_url, email_to, created_at, updated_at
		FROM alert_configs
		WHERE enabled = TRUE AND conditions @> $1::jsonb`,
		`["`+condition+`"]`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var configs []AlertConfig
	for rows.Next() {
		var a AlertConfig
		if err := scanAlertConfigRow(rows, &a); err != nil {
			return nil, err
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ─── Active Alerts ────────────────────────────────────────────────────────────

// FireAlert inserts a new active alert. If an identical unresolved alert already
// exists (same cluster+node+condition) it is a no-op and the existing row is returned.
func (q *AlertQuerier) FireAlert(ctx context.Context, clusterID, nodeID *uuid.UUID, severity, condition, message string) (*ActiveAlert, error) {
	// Upsert: don't create duplicates for the same ongoing condition.
	var a ActiveAlert
	row := q.pool.QueryRow(ctx, `
		INSERT INTO active_alerts (cluster_id, node_id, severity, condition, message)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
		RETURNING id, cluster_id, node_id, severity, condition, message, fired_at, resolved_at, acknowledged_at`,
		clusterID, nodeID, severity, condition, message,
	)
	if err := row.Scan(
		&a.ID, &a.ClusterID, &a.NodeID, &a.Severity, &a.Condition, &a.Message,
		&a.FiredAt, &a.ResolvedAt, &a.AcknowledgedAt,
	); err != nil {
		// ON CONFLICT DO NOTHING returns no row — fetch the existing one.
		existRow := q.pool.QueryRow(ctx, `
			SELECT id, cluster_id, node_id, severity, condition, message, fired_at, resolved_at, acknowledged_at
			FROM active_alerts
			WHERE cluster_id IS NOT DISTINCT FROM $1
			  AND node_id    IS NOT DISTINCT FROM $2
			  AND condition = $3
			  AND resolved_at IS NULL
			LIMIT 1`,
			clusterID, nodeID, condition,
		)
		if err2 := existRow.Scan(
			&a.ID, &a.ClusterID, &a.NodeID, &a.Severity, &a.Condition, &a.Message,
			&a.FiredAt, &a.ResolvedAt, &a.AcknowledgedAt,
		); err2 != nil {
			return nil, err2
		}
	}
	return &a, nil
}

// ResolveAlert marks all unresolved alerts for a given cluster+node+condition as resolved.
func (q *AlertQuerier) ResolveAlert(ctx context.Context, clusterID, nodeID *uuid.UUID, condition string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE active_alerts
		SET resolved_at = now()
		WHERE cluster_id IS NOT DISTINCT FROM $1
		  AND node_id    IS NOT DISTINCT FROM $2
		  AND condition  = $3
		  AND resolved_at IS NULL`,
		clusterID, nodeID, condition,
	)
	return err
}

// AcknowledgeAlert marks a single alert as acknowledged.
func (q *AlertQuerier) AcknowledgeAlert(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE active_alerts SET acknowledged_at = now() WHERE id = $1`, id)
	return err
}

// ListActive returns all currently unresolved alerts, newest first.
func (q *AlertQuerier) ListActive(ctx context.Context) ([]ActiveAlert, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, node_id, severity, condition, message,
		       fired_at, resolved_at, acknowledged_at
		FROM active_alerts
		WHERE resolved_at IS NULL
		ORDER BY fired_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActiveAlerts(rows)
}

// ListRecentByCluster returns the last 100 alerts (resolved or not) for a cluster.
func (q *AlertQuerier) ListRecentByCluster(ctx context.Context, clusterID uuid.UUID) ([]ActiveAlert, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, node_id, severity, condition, message,
		       fired_at, resolved_at, acknowledged_at
		FROM active_alerts
		WHERE cluster_id = $1
		ORDER BY fired_at DESC
		LIMIT 100`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActiveAlerts(rows)
}

// ─── scan helpers ─────────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

func scanAlertConfig(row scanner, a *AlertConfig) error {
	var condJSON []byte
	if err := row.Scan(
		&a.ID, &a.Name, &a.Type, &a.Enabled, &condJSON,
		&a.WebhookURL, &a.EmailTo, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return err
	}
	_ = json.Unmarshal(condJSON, &a.Conditions)
	if a.Conditions == nil {
		a.Conditions = []string{}
	}
	return nil
}

type rowsScanner interface {
	Scan(dest ...any) error
}

func scanAlertConfigRow(row rowsScanner, a *AlertConfig) error {
	var condJSON []byte
	if err := row.Scan(
		&a.ID, &a.Name, &a.Type, &a.Enabled, &condJSON,
		&a.WebhookURL, &a.EmailTo, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return err
	}
	_ = json.Unmarshal(condJSON, &a.Conditions)
	if a.Conditions == nil {
		a.Conditions = []string{}
	}
	return nil
}

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

func scanActiveAlerts(rows pgxRows) ([]ActiveAlert, error) {
	defer rows.Close()
	var alerts []ActiveAlert
	for rows.Next() {
		var a ActiveAlert
		if err := rows.Scan(
			&a.ID, &a.ClusterID, &a.NodeID, &a.Severity, &a.Condition, &a.Message,
			&a.FiredAt, &a.ResolvedAt, &a.AcknowledgedAt,
		); err != nil {
			return nil, err
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}
