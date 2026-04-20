package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertTransport is a delivery channel for alert notifications.
type AlertTransport struct {
	ID        uuid.UUID              `json:"id"`
	Name      string                 `json:"name"`
	Type      string                 `json:"type"` // webhook | email | slack_webhook | slack_bot
	Config    map[string]interface{} `json:"config"`
	Enabled   bool                   `json:"enabled"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// AlertTransportParams holds the fields for creating or updating a transport.
type AlertTransportParams struct {
	Name    string
	Type    string
	Config  map[string]interface{}
	Enabled bool
}

// AlertTransportQuerier performs alert_transports operations.
type AlertTransportQuerier struct {
	pool *pgxpool.Pool
}

func NewAlertTransportQuerier(pool *pgxpool.Pool) *AlertTransportQuerier {
	return &AlertTransportQuerier{pool: pool}
}

func (q *AlertTransportQuerier) Create(ctx context.Context, p AlertTransportParams) (*AlertTransport, error) {
	cfgJSON, _ := json.Marshal(p.Config)
	var t AlertTransport
	row := q.pool.QueryRow(ctx, `
		INSERT INTO alert_transports (name, type, config, enabled)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, type, config, enabled, created_at, updated_at`,
		p.Name, p.Type, cfgJSON, p.Enabled,
	)
	return &t, scanTransport(row, &t)
}

func (q *AlertTransportQuerier) List(ctx context.Context) ([]AlertTransport, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, type, config, enabled, created_at, updated_at
		FROM alert_transports ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AlertTransport
	for rows.Next() {
		var t AlertTransport
		if err := scanTransportRow(rows, &t); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (q *AlertTransportQuerier) GetByID(ctx context.Context, id uuid.UUID) (*AlertTransport, error) {
	var t AlertTransport
	row := q.pool.QueryRow(ctx, `
		SELECT id, name, type, config, enabled, created_at, updated_at
		FROM alert_transports WHERE id = $1`, id)
	return &t, scanTransport(row, &t)
}

func (q *AlertTransportQuerier) Update(ctx context.Context, id uuid.UUID, p AlertTransportParams) (*AlertTransport, error) {
	cfgJSON, _ := json.Marshal(p.Config)
	var t AlertTransport
	row := q.pool.QueryRow(ctx, `
		UPDATE alert_transports
		SET name = $2, type = $3, config = $4, enabled = $5, updated_at = now()
		WHERE id = $1
		RETURNING id, name, type, config, enabled, created_at, updated_at`,
		id, p.Name, p.Type, cfgJSON, p.Enabled,
	)
	return &t, scanTransport(row, &t)
}

func (q *AlertTransportQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM alert_transports WHERE id = $1`, id)
	return err
}

// ─── scan helpers ─────────────────────────────────────────────────────────────

type rowScanner interface{ Scan(dest ...any) error }

func scanTransport(row rowScanner, t *AlertTransport) error {
	var cfgJSON []byte
	if err := row.Scan(&t.ID, &t.Name, &t.Type, &cfgJSON, &t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	if cfgJSON != nil {
		_ = json.Unmarshal(cfgJSON, &t.Config)
	}
	return nil
}

func scanTransportRow(row rowScanner, t *AlertTransport) error {
	return scanTransport(row, t)
}
