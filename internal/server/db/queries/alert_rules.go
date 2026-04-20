package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertRule defines what events trigger an alert and how the alert behaves.
type AlertRule struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`

	// Match conditions
	Categories   []string `json:"categories"`
	Codes        []string `json:"codes"`
	MinSeverity  string   `json:"min_severity"`
	MessageRegex *string  `json:"message_regex,omitempty"`

	// Metric threshold (optional)
	MetricField    *string  `json:"metric_field,omitempty"`
	MetricOperator *string  `json:"metric_operator,omitempty"`
	MetricValue    *float64 `json:"metric_value,omitempty"`

	// Scope
	ClusterID *uuid.UUID `json:"cluster_id,omitempty"`
	NodeID    *uuid.UUID `json:"node_id,omitempty"`

	// Re-alert behavior
	FireMode      string `json:"fire_mode"` // once | re_alert | every_occurrence
	ReAlertMins   *int   `json:"re_alert_mins,omitempty"`
	MaxReAlerts   *int   `json:"max_re_alerts,omitempty"`
	NotifyOnClear bool   `json:"notify_on_clear"`

	// Escalation
	EscalateAfterMins   *int       `json:"escalate_after_mins,omitempty"`
	EscalateTransportID *uuid.UUID `json:"escalate_transport_id,omitempty"`

	// Schedule
	ScheduleID *uuid.UUID `json:"schedule_id,omitempty"`

	// Transport IDs (populated by ListWithTransports)
	TransportIDs []uuid.UUID `json:"transport_ids"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AlertRuleParams holds the fields for creating or updating a rule.
type AlertRuleParams struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`

	Categories   []string `json:"categories"`
	Codes        []string `json:"codes"`
	MinSeverity  string   `json:"min_severity"`
	MessageRegex *string  `json:"message_regex"`

	MetricField    *string  `json:"metric_field"`
	MetricOperator *string  `json:"metric_operator"`
	MetricValue    *float64 `json:"metric_value"`

	ClusterID *uuid.UUID `json:"cluster_id"`
	NodeID    *uuid.UUID `json:"node_id"`

	FireMode      string `json:"fire_mode"`
	ReAlertMins   *int   `json:"re_alert_mins"`
	MaxReAlerts   *int   `json:"max_re_alerts"`
	NotifyOnClear bool   `json:"notify_on_clear"`

	EscalateAfterMins   *int       `json:"escalate_after_mins"`
	EscalateTransportID *uuid.UUID `json:"escalate_transport_id"`

	ScheduleID *uuid.UUID `json:"schedule_id"`

	TransportIDs []uuid.UUID `json:"transport_ids"`
}

// AlertRuleQuerier performs alert_rules operations.
type AlertRuleQuerier struct {
	pool *pgxpool.Pool
}

func NewAlertRuleQuerier(pool *pgxpool.Pool) *AlertRuleQuerier {
	return &AlertRuleQuerier{pool: pool}
}

func (q *AlertRuleQuerier) Create(ctx context.Context, p AlertRuleParams) (*AlertRule, error) {
	var r AlertRule
	row := q.pool.QueryRow(ctx, `
		INSERT INTO alert_rules
			(name, description, enabled,
			 categories, codes, min_severity, message_regex,
			 metric_field, metric_operator, metric_value,
			 cluster_id, node_id,
			 fire_mode, re_alert_mins, max_re_alerts, notify_on_clear,
			 escalate_after_mins, escalate_transport_id, schedule_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING id, name, description, enabled,
		          categories, codes, min_severity, message_regex,
		          metric_field, metric_operator, metric_value,
		          cluster_id, node_id,
		          fire_mode, re_alert_mins, max_re_alerts, notify_on_clear,
		          escalate_after_mins, escalate_transport_id, schedule_id,
		          created_at, updated_at`,
		p.Name, p.Description, p.Enabled,
		p.Categories, p.Codes, p.MinSeverity, p.MessageRegex,
		p.MetricField, p.MetricOperator, p.MetricValue,
		p.ClusterID, p.NodeID,
		p.FireMode, p.ReAlertMins, p.MaxReAlerts, p.NotifyOnClear,
		p.EscalateAfterMins, p.EscalateTransportID, p.ScheduleID,
	)
	if err := scanRule(row, &r); err != nil {
		return nil, err
	}
	if err := q.setTransports(ctx, r.ID, p.TransportIDs); err != nil {
		return nil, err
	}
	r.TransportIDs = p.TransportIDs
	return &r, nil
}

func (q *AlertRuleQuerier) List(ctx context.Context) ([]AlertRule, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, description, enabled,
		       categories, codes, min_severity, message_regex,
		       metric_field, metric_operator, metric_value,
		       cluster_id, node_id,
		       fire_mode, re_alert_mins, max_re_alerts, notify_on_clear,
		       escalate_after_mins, escalate_transport_id, schedule_id,
		       created_at, updated_at
		FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AlertRule
	for rows.Next() {
		var r AlertRule
		if err := scanRuleRow(rows, &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := q.loadTransportIDs(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (q *AlertRuleQuerier) GetByID(ctx context.Context, id uuid.UUID) (*AlertRule, error) {
	var r AlertRule
	row := q.pool.QueryRow(ctx, `
		SELECT id, name, description, enabled,
		       categories, codes, min_severity, message_regex,
		       metric_field, metric_operator, metric_value,
		       cluster_id, node_id,
		       fire_mode, re_alert_mins, max_re_alerts, notify_on_clear,
		       escalate_after_mins, escalate_transport_id, schedule_id,
		       created_at, updated_at
		FROM alert_rules WHERE id = $1`, id)
	if err := scanRule(row, &r); err != nil {
		return nil, err
	}
	tids, err := q.getTransportIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	r.TransportIDs = tids
	return &r, nil
}

func (q *AlertRuleQuerier) Update(ctx context.Context, id uuid.UUID, p AlertRuleParams) (*AlertRule, error) {
	var r AlertRule
	row := q.pool.QueryRow(ctx, `
		UPDATE alert_rules SET
			name = $2, description = $3, enabled = $4,
			categories = $5, codes = $6, min_severity = $7, message_regex = $8,
			metric_field = $9, metric_operator = $10, metric_value = $11,
			cluster_id = $12, node_id = $13,
			fire_mode = $14, re_alert_mins = $15, max_re_alerts = $16, notify_on_clear = $17,
			escalate_after_mins = $18, escalate_transport_id = $19, schedule_id = $20,
			updated_at = now()
		WHERE id = $1
		RETURNING id, name, description, enabled,
		          categories, codes, min_severity, message_regex,
		          metric_field, metric_operator, metric_value,
		          cluster_id, node_id,
		          fire_mode, re_alert_mins, max_re_alerts, notify_on_clear,
		          escalate_after_mins, escalate_transport_id, schedule_id,
		          created_at, updated_at`,
		id,
		p.Name, p.Description, p.Enabled,
		p.Categories, p.Codes, p.MinSeverity, p.MessageRegex,
		p.MetricField, p.MetricOperator, p.MetricValue,
		p.ClusterID, p.NodeID,
		p.FireMode, p.ReAlertMins, p.MaxReAlerts, p.NotifyOnClear,
		p.EscalateAfterMins, p.EscalateTransportID, p.ScheduleID,
	)
	if err := scanRule(row, &r); err != nil {
		return nil, err
	}
	if err := q.setTransports(ctx, id, p.TransportIDs); err != nil {
		return nil, err
	}
	r.TransportIDs = p.TransportIDs
	return &r, nil
}

func (q *AlertRuleQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
	return err
}

// ListEnabled returns all enabled rules with their transport IDs.
// Used by the alert engine's reload loop.
func (q *AlertRuleQuerier) ListEnabled(ctx context.Context) ([]AlertRule, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, description, enabled,
		       categories, codes, min_severity, message_regex,
		       metric_field, metric_operator, metric_value,
		       cluster_id, node_id,
		       fire_mode, re_alert_mins, max_re_alerts, notify_on_clear,
		       escalate_after_mins, escalate_transport_id, schedule_id,
		       created_at, updated_at
		FROM alert_rules WHERE enabled = TRUE ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AlertRule
	for rows.Next() {
		var r AlertRule
		if err := scanRuleRow(rows, &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := q.loadTransportIDs(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

// ─── transport junction helpers ───────────────────────────────────────────────

func (q *AlertRuleQuerier) setTransports(ctx context.Context, ruleID uuid.UUID, tids []uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM alert_rule_transports WHERE rule_id = $1`, ruleID)
	if err != nil {
		return err
	}
	for _, tid := range tids {
		if _, err := q.pool.Exec(ctx,
			`INSERT INTO alert_rule_transports (rule_id, transport_id) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, ruleID, tid,
		); err != nil {
			return err
		}
	}
	return nil
}

func (q *AlertRuleQuerier) getTransportIDs(ctx context.Context, ruleID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT transport_id FROM alert_rule_transports WHERE rule_id = $1`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (q *AlertRuleQuerier) loadTransportIDs(ctx context.Context, rules []AlertRule) error {
	for i := range rules {
		tids, err := q.getTransportIDs(ctx, rules[i].ID)
		if err != nil {
			return err
		}
		rules[i].TransportIDs = tids
	}
	return nil
}

// ─── scan helpers ─────────────────────────────────────────────────────────────

func scanRule(row rowScanner, r *AlertRule) error {
	return row.Scan(
		&r.ID, &r.Name, &r.Description, &r.Enabled,
		&r.Categories, &r.Codes, &r.MinSeverity, &r.MessageRegex,
		&r.MetricField, &r.MetricOperator, &r.MetricValue,
		&r.ClusterID, &r.NodeID,
		&r.FireMode, &r.ReAlertMins, &r.MaxReAlerts, &r.NotifyOnClear,
		&r.EscalateAfterMins, &r.EscalateTransportID, &r.ScheduleID,
		&r.CreatedAt, &r.UpdatedAt,
	)
}

func scanRuleRow(row rowScanner, r *AlertRule) error {
	return scanRule(row, r)
}
