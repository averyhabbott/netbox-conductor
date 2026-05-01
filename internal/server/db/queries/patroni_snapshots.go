package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PatroniConfigSnapshot is one captured DCS config state for a cluster.
type PatroniConfigSnapshot struct {
	ID         uuid.UUID
	ClusterID  uuid.UUID
	CapturedAt time.Time
	Source     string
	Config     json.RawMessage
}

type PatroniSnapshotQuerier struct {
	pool *pgxpool.Pool
}

func NewPatroniSnapshotQuerier(pool *pgxpool.Pool) *PatroniSnapshotQuerier {
	return &PatroniSnapshotQuerier{pool: pool}
}

// Insert stores a new DCS config snapshot. The config argument must be valid JSON.
func (q *PatroniSnapshotQuerier) Insert(ctx context.Context, clusterID uuid.UUID, source string, config []byte) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO patroni_config_snapshots (cluster_id, source, config)
		VALUES ($1, $2, $3)
	`, clusterID, source, config)
	return err
}

// List returns the 10 most recent snapshots for a cluster, newest first.
func (q *PatroniSnapshotQuerier) List(ctx context.Context, clusterID uuid.UUID) ([]PatroniConfigSnapshot, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, captured_at, source, config
		FROM patroni_config_snapshots
		WHERE cluster_id = $1
		ORDER BY captured_at DESC
		LIMIT 10
	`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []PatroniConfigSnapshot
	for rows.Next() {
		var s PatroniConfigSnapshot
		if err := rows.Scan(&s.ID, &s.ClusterID, &s.CapturedAt, &s.Source, &s.Config); err != nil {
			return nil, err
		}
		snaps = append(snaps, s)
	}
	return snaps, rows.Err()
}

// Prune deletes all but the 10 most recent snapshots for a cluster.
func (q *PatroniSnapshotQuerier) Prune(ctx context.Context, clusterID uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		DELETE FROM patroni_config_snapshots
		WHERE cluster_id = $1
		  AND id NOT IN (
			SELECT id FROM patroni_config_snapshots
			WHERE cluster_id = $1
			ORDER BY captured_at DESC
			LIMIT 10
		  )
	`, clusterID)
	return err
}
