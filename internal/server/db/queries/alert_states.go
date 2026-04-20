package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertState tracks the current lifecycle state of an alert rule match.
type AlertState struct {
	ID             uuid.UUID  `json:"id"`
	RuleID         uuid.UUID  `json:"rule_id"`
	RuleName       *string    `json:"rule_name,omitempty"`
	ClusterID      *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName    *string    `json:"cluster_name,omitempty"`
	NodeID         *uuid.UUID `json:"node_id,omitempty"`
	NodeName       *string    `json:"node_name,omitempty"`
	State          string     `json:"state"` // active | resolved | acknowledged
	ReAlertCount   int        `json:"re_alert_count"`
	Escalated      bool       `json:"escalated"`
	FirstFiredAt   time.Time  `json:"first_fired_at"`
	LastFiredAt    time.Time  `json:"last_fired_at"`
	LastAlertedAt  *time.Time `json:"last_alerted_at,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy *uuid.UUID `json:"acknowledged_by,omitempty"`
}

// AlertStateQuerier performs active_alert_states operations.
type AlertStateQuerier struct {
	pool *pgxpool.Pool
}

func NewAlertStateQuerier(pool *pgxpool.Pool) *AlertStateQuerier {
	return &AlertStateQuerier{pool: pool}
}

// UpsertActive inserts a new active state or returns the existing one.
// Returns (state, isNew, error).
func (q *AlertStateQuerier) UpsertActive(ctx context.Context, ruleID uuid.UUID, clusterID, nodeID *uuid.UUID) (*AlertState, bool, error) {
	var s AlertState
	err := q.pool.QueryRow(ctx, `
		INSERT INTO active_alert_states (rule_id, cluster_id, node_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
		RETURNING id, rule_id, cluster_id, node_id, state, re_alert_count, escalated,
		          first_fired_at, last_fired_at, last_alerted_at,
		          resolved_at, acknowledged_at, acknowledged_by`,
		ruleID, clusterID, nodeID,
	).Scan(
		&s.ID, &s.RuleID, &s.ClusterID, &s.NodeID, &s.State,
		&s.ReAlertCount, &s.Escalated,
		&s.FirstFiredAt, &s.LastFiredAt, &s.LastAlertedAt,
		&s.ResolvedAt, &s.AcknowledgedAt, &s.AcknowledgedBy,
	)
	if err == nil {
		return &s, true, nil
	}
	// Row already existed — fetch it.
	err = q.pool.QueryRow(ctx, `
		SELECT id, rule_id, cluster_id, node_id, state, re_alert_count, escalated,
		       first_fired_at, last_fired_at, last_alerted_at,
		       resolved_at, acknowledged_at, acknowledged_by
		FROM active_alert_states
		WHERE rule_id     = $1
		  AND cluster_id  IS NOT DISTINCT FROM $2
		  AND node_id     IS NOT DISTINCT FROM $3
		  AND state      != 'resolved'
		LIMIT 1`,
		ruleID, clusterID, nodeID,
	).Scan(
		&s.ID, &s.RuleID, &s.ClusterID, &s.NodeID, &s.State,
		&s.ReAlertCount, &s.Escalated,
		&s.FirstFiredAt, &s.LastFiredAt, &s.LastAlertedAt,
		&s.ResolvedAt, &s.AcknowledgedAt, &s.AcknowledgedBy,
	)
	return &s, false, err
}

// MarkAlerted updates last_fired_at, last_alerted_at, and increments re_alert_count.
func (q *AlertStateQuerier) MarkAlerted(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE active_alert_states
		SET last_fired_at = now(), last_alerted_at = now(), re_alert_count = re_alert_count + 1
		WHERE id = $1`, id)
	return err
}

// MarkEscalated sets escalated = true.
func (q *AlertStateQuerier) MarkEscalated(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE active_alert_states SET escalated = TRUE WHERE id = $1`, id)
	return err
}

// Resolve marks an alert state as resolved.
func (q *AlertStateQuerier) Resolve(ctx context.Context, ruleID uuid.UUID, clusterID, nodeID *uuid.UUID) (*AlertState, error) {
	var s AlertState
	err := q.pool.QueryRow(ctx, `
		UPDATE active_alert_states
		SET state = 'resolved', resolved_at = now()
		WHERE rule_id    = $1
		  AND cluster_id IS NOT DISTINCT FROM $2
		  AND node_id    IS NOT DISTINCT FROM $3
		  AND state      = 'active'
		RETURNING id, rule_id, cluster_id, node_id, state, re_alert_count, escalated,
		          first_fired_at, last_fired_at, last_alerted_at,
		          resolved_at, acknowledged_at, acknowledged_by`,
		ruleID, clusterID, nodeID,
	).Scan(
		&s.ID, &s.RuleID, &s.ClusterID, &s.NodeID, &s.State,
		&s.ReAlertCount, &s.Escalated,
		&s.FirstFiredAt, &s.LastFiredAt, &s.LastAlertedAt,
		&s.ResolvedAt, &s.AcknowledgedAt, &s.AcknowledgedBy,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ResolveByID marks a specific active alert state as resolved by its state ID.
func (q *AlertStateQuerier) ResolveByID(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE active_alert_states
		SET state = 'resolved', resolved_at = now()
		WHERE id = $1 AND state != 'resolved'`, id)
	return err
}

// Acknowledge marks an active alert state as acknowledged.
func (q *AlertStateQuerier) Acknowledge(ctx context.Context, id, userID uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE active_alert_states
		SET state = 'acknowledged', acknowledged_at = now(), acknowledged_by = $2
		WHERE id = $1`, id, userID)
	return err
}

// ListActive returns all non-resolved alert states joined with rule, cluster, and node names.
func (q *AlertStateQuerier) ListActive(ctx context.Context) ([]AlertState, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT s.id, s.rule_id, s.cluster_id, s.node_id, s.state, s.re_alert_count, s.escalated,
		       s.first_fired_at, s.last_fired_at, s.last_alerted_at,
		       s.resolved_at, s.acknowledged_at, s.acknowledged_by,
		       r.name AS rule_name,
		       c.name AS cluster_name,
		       n.hostname AS node_name
		FROM active_alert_states s
		LEFT JOIN alert_rules r ON r.id = s.rule_id
		LEFT JOIN clusters c ON c.id = s.cluster_id
		LEFT JOIN nodes n ON n.id = s.node_id
		WHERE s.state != 'resolved'
		ORDER BY s.first_fired_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AlertState
	for rows.Next() {
		var s AlertState
		if err := rows.Scan(
			&s.ID, &s.RuleID, &s.ClusterID, &s.NodeID, &s.State,
			&s.ReAlertCount, &s.Escalated,
			&s.FirstFiredAt, &s.LastFiredAt, &s.LastAlertedAt,
			&s.ResolvedAt, &s.AcknowledgedAt, &s.AcknowledgedBy,
			&s.RuleName, &s.ClusterName, &s.NodeName,
		); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
