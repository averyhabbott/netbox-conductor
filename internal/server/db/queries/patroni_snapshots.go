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
	IsActive   bool
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

// InsertActive stores a new DCS config snapshot and marks it as active,
// clearing is_active on all other snapshots for this cluster atomically.
func (q *PatroniSnapshotQuerier) InsertActive(ctx context.Context, clusterID uuid.UUID, source string, config []byte) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE patroni_config_snapshots SET is_active = false WHERE cluster_id = $1
	`, clusterID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO patroni_config_snapshots (cluster_id, source, config, is_active)
		VALUES ($1, $2, $3, true)
	`, clusterID, source, config); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetActive marks the given snapshot as active and clears all others for the cluster.
func (q *PatroniSnapshotQuerier) SetActive(ctx context.Context, id uuid.UUID, clusterID uuid.UUID) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE patroni_config_snapshots SET is_active = false WHERE cluster_id = $1
	`, clusterID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE patroni_config_snapshots SET is_active = true WHERE id = $1
	`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// List returns the 10 most recent snapshots for a cluster, newest first.
func (q *PatroniSnapshotQuerier) List(ctx context.Context, clusterID uuid.UUID) ([]PatroniConfigSnapshot, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, captured_at, source, config, is_active
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
		if err := rows.Scan(&s.ID, &s.ClusterID, &s.CapturedAt, &s.Source, &s.Config, &s.IsActive); err != nil {
			return nil, err
		}
		snaps = append(snaps, s)
	}
	return snaps, rows.Err()
}

// GetActive returns the currently active snapshot for a cluster, or nil if none.
func (q *PatroniSnapshotQuerier) GetActive(ctx context.Context, clusterID uuid.UUID) (*PatroniConfigSnapshot, error) {
	var s PatroniConfigSnapshot
	err := q.pool.QueryRow(ctx, `
		SELECT id, cluster_id, captured_at, source, config, is_active
		FROM patroni_config_snapshots
		WHERE cluster_id = $1 AND is_active = true
	`, clusterID).Scan(&s.ID, &s.ClusterID, &s.CapturedAt, &s.Source, &s.Config, &s.IsActive)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetByID returns a single snapshot by ID.
func (q *PatroniSnapshotQuerier) GetByID(ctx context.Context, id uuid.UUID) (*PatroniConfigSnapshot, error) {
	var s PatroniConfigSnapshot
	err := q.pool.QueryRow(ctx, `
		SELECT id, cluster_id, captured_at, source, config, is_active
		FROM patroni_config_snapshots
		WHERE id = $1
	`, id).Scan(&s.ID, &s.ClusterID, &s.CapturedAt, &s.Source, &s.Config, &s.IsActive)
	if err != nil {
		return nil, err
	}
	return &s, nil
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
