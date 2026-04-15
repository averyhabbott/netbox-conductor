package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionPolicy represents a cluster's backup retention configuration.
type RetentionPolicy struct {
	ClusterID     uuid.UUID
	RetentionDays int
	ExpireCmd     string // empty = use default pgbackrest expire
	UpdatedAt     time.Time
}

// RetentionQuerier performs retention-policy DB operations.
type RetentionQuerier struct {
	pool *pgxpool.Pool
}

func NewRetentionQuerier(pool *pgxpool.Pool) *RetentionQuerier {
	return &RetentionQuerier{pool: pool}
}

func (q *RetentionQuerier) Get(ctx context.Context, clusterID uuid.UUID) (*RetentionPolicy, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT cluster_id, retention_days, COALESCE(expire_cmd, ''), updated_at
		FROM retention_policies WHERE cluster_id = $1
	`, clusterID)

	var p RetentionPolicy
	if err := row.Scan(&p.ClusterID, &p.RetentionDays, &p.ExpireCmd, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func (q *RetentionQuerier) Upsert(ctx context.Context, clusterID uuid.UUID, retentionDays int, expireCmd string) (*RetentionPolicy, error) {
	var cmd *string
	if expireCmd != "" {
		cmd = &expireCmd
	}
	row := q.pool.QueryRow(ctx, `
		INSERT INTO retention_policies (cluster_id, retention_days, expire_cmd, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (cluster_id) DO UPDATE
		  SET retention_days = EXCLUDED.retention_days,
		      expire_cmd     = EXCLUDED.expire_cmd,
		      updated_at     = now()
		RETURNING cluster_id, retention_days, COALESCE(expire_cmd, ''), updated_at
	`, clusterID, retentionDays, cmd)

	var p RetentionPolicy
	if err := row.Scan(&p.ClusterID, &p.RetentionDays, &p.ExpireCmd, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}
