package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScheduleWindow defines a day-of-week + hour range during which an alert rule is active.
// Days: 0=Sunday, 1=Monday ... 6=Saturday (matches time.Weekday).
type ScheduleWindow struct {
	Days  []int  `json:"days"`
	Start string `json:"start"` // "HH:MM" in 24h format
	End   string `json:"end"`   // "HH:MM" in 24h format
}

// AlertSchedule is a named, reusable collection of time windows.
type AlertSchedule struct {
	ID        uuid.UUID        `json:"id"`
	Name      string           `json:"name"`
	Timezone  string           `json:"timezone"`
	Windows   []ScheduleWindow `json:"windows"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// AlertScheduleParams holds the fields for creating or updating a schedule.
type AlertScheduleParams struct {
	Name     string
	Timezone string
	Windows  []ScheduleWindow
}

// AlertScheduleQuerier performs alert_schedules operations.
type AlertScheduleQuerier struct {
	pool *pgxpool.Pool
}

func NewAlertScheduleQuerier(pool *pgxpool.Pool) *AlertScheduleQuerier {
	return &AlertScheduleQuerier{pool: pool}
}

func (q *AlertScheduleQuerier) Create(ctx context.Context, p AlertScheduleParams) (*AlertSchedule, error) {
	winJSON, _ := json.Marshal(p.Windows)
	var s AlertSchedule
	row := q.pool.QueryRow(ctx, `
		INSERT INTO alert_schedules (name, timezone, windows)
		VALUES ($1, $2, $3)
		RETURNING id, name, timezone, windows, created_at, updated_at`,
		p.Name, p.Timezone, winJSON,
	)
	return &s, scanSchedule(row, &s)
}

func (q *AlertScheduleQuerier) List(ctx context.Context) ([]AlertSchedule, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, timezone, windows, created_at, updated_at
		FROM alert_schedules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AlertSchedule
	for rows.Next() {
		var s AlertSchedule
		if err := scanSchedule(rows, &s); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (q *AlertScheduleQuerier) GetByID(ctx context.Context, id uuid.UUID) (*AlertSchedule, error) {
	var s AlertSchedule
	row := q.pool.QueryRow(ctx, `
		SELECT id, name, timezone, windows, created_at, updated_at
		FROM alert_schedules WHERE id = $1`, id)
	return &s, scanSchedule(row, &s)
}

func (q *AlertScheduleQuerier) Update(ctx context.Context, id uuid.UUID, p AlertScheduleParams) (*AlertSchedule, error) {
	winJSON, _ := json.Marshal(p.Windows)
	var s AlertSchedule
	row := q.pool.QueryRow(ctx, `
		UPDATE alert_schedules
		SET name = $2, timezone = $3, windows = $4, updated_at = now()
		WHERE id = $1
		RETURNING id, name, timezone, windows, created_at, updated_at`,
		id, p.Name, p.Timezone, winJSON,
	)
	return &s, scanSchedule(row, &s)
}

func (q *AlertScheduleQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM alert_schedules WHERE id = $1`, id)
	return err
}

func scanSchedule(row rowScanner, s *AlertSchedule) error {
	var winJSON []byte
	if err := row.Scan(&s.ID, &s.Name, &s.Timezone, &winJSON, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return err
	}
	if winJSON != nil {
		_ = json.Unmarshal(winJSON, &s.Windows)
	}
	if s.Windows == nil {
		s.Windows = []ScheduleWindow{}
	}
	return nil
}
