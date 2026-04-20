package queries

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventRetentionPolicy holds the retention configuration for one event category.
type EventRetentionPolicy struct {
	Category   string    `json:"category"`
	RetainDays int       `json:"retain_days"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// EventRetentionQuerier performs event_retention operations.
type EventRetentionQuerier struct {
	pool *pgxpool.Pool
}

func NewEventRetentionQuerier(pool *pgxpool.Pool) *EventRetentionQuerier {
	return &EventRetentionQuerier{pool: pool}
}

// GetAll returns all per-category retention policies.
func (q *EventRetentionQuerier) GetAll(ctx context.Context) ([]EventRetentionPolicy, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT category, retain_days, updated_at
		FROM event_retention
		ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EventRetentionPolicy
	for rows.Next() {
		var p EventRetentionPolicy
		if err := rows.Scan(&p.Category, &p.RetainDays, &p.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// Update sets the retain_days for a specific category.
func (q *EventRetentionQuerier) Update(ctx context.Context, category string, retainDays int) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO event_retention (category, retain_days)
		VALUES ($1, $2)
		ON CONFLICT (category) DO UPDATE
		SET retain_days = EXCLUDED.retain_days, updated_at = now()`,
		category, retainDays,
	)
	return err
}

// UpdateAll replaces the retention policy for every category at once.
func (q *EventRetentionQuerier) UpdateAll(ctx context.Context, policies []EventRetentionPolicy) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range policies {
		if _, err := tx.Exec(ctx, `
			INSERT INTO event_retention (category, retain_days)
			VALUES ($1, $2)
			ON CONFLICT (category) DO UPDATE
			SET retain_days = EXCLUDED.retain_days, updated_at = now()`,
			p.Category, p.RetainDays,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
