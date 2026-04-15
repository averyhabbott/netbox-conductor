package queries

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NetboxConfig represents a versioned configuration snapshot for a cluster.
type NetboxConfig struct {
	ID             uuid.UUID
	ClusterID      uuid.UUID
	Version        int
	ConfigTemplate string
	RenderedHash   *string
	PushedAt       *time.Time
	PushStatus     *string
	CreatedAt      time.Time
}

// ConfigOverride is a per-node key=value appended to the rendered config.
type ConfigOverride struct {
	ID       uuid.UUID
	ConfigID uuid.UUID
	NodeID   uuid.UUID
	Key      string
	Value    string
}

// ConfigQuerier provides DB access for netbox_configs and netbox_config_overrides.
type ConfigQuerier struct {
	pool *pgxpool.Pool
}

func NewConfigQuerier(pool *pgxpool.Pool) *ConfigQuerier {
	return &ConfigQuerier{pool: pool}
}

// GetLatest returns the highest-version config for a cluster, or nil if none.
func (q *ConfigQuerier) GetLatest(ctx context.Context, clusterID uuid.UUID) (*NetboxConfig, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, cluster_id, version, config_template, rendered_hash,
		       pushed_at, push_status, created_at
		FROM netbox_configs
		WHERE cluster_id = $1
		ORDER BY version DESC
		LIMIT 1`,
		clusterID)
	return scanConfig(row)
}

// GetByVersion returns a specific version of a cluster's config.
func (q *ConfigQuerier) GetByVersion(ctx context.Context, clusterID uuid.UUID, version int) (*NetboxConfig, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, cluster_id, version, config_template, rendered_hash,
		       pushed_at, push_status, created_at
		FROM netbox_configs
		WHERE cluster_id = $1 AND version = $2`,
		clusterID, version)
	return scanConfig(row)
}

// Create inserts a new config version (version = MAX(existing) + 1).
func (q *ConfigQuerier) Create(ctx context.Context, clusterID uuid.UUID, tmpl string) (*NetboxConfig, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO netbox_configs (cluster_id, version, config_template)
		VALUES (
			$1,
			COALESCE((SELECT MAX(version) FROM netbox_configs WHERE cluster_id = $1), 0) + 1,
			$2
		)
		RETURNING id, cluster_id, version, config_template, rendered_hash,
		          pushed_at, push_status, created_at`,
		clusterID, tmpl)
	return scanConfig(row)
}

// UpdatePushStatus updates push_status, rendered_hash, and pushed_at.
func (q *ConfigQuerier) UpdatePushStatus(ctx context.Context, id uuid.UUID, status, hash string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE netbox_configs
		SET push_status = $2, rendered_hash = $3, pushed_at = now()
		WHERE id = $1`,
		id, status, hash)
	return err
}

// List returns all config versions for a cluster, newest first.
func (q *ConfigQuerier) List(ctx context.Context, clusterID uuid.UUID) ([]NetboxConfig, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, version, config_template, rendered_hash,
		       pushed_at, push_status, created_at
		FROM netbox_configs
		WHERE cluster_id = $1
		ORDER BY version DESC`,
		clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []NetboxConfig
	for rows.Next() {
		var c NetboxConfig
		if err := rows.Scan(&c.ID, &c.ClusterID, &c.Version, &c.ConfigTemplate,
			&c.RenderedHash, &c.PushedAt, &c.PushStatus, &c.CreatedAt); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// ── Overrides ─────────────────────────────────────────────────────────────────

// ListOverrides returns all overrides for a config version.
func (q *ConfigQuerier) ListOverrides(ctx context.Context, configID uuid.UUID) ([]ConfigOverride, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, config_id, node_id, key, value
		FROM netbox_config_overrides
		WHERE config_id = $1
		ORDER BY node_id, key`,
		configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var overrides []ConfigOverride
	for rows.Next() {
		var o ConfigOverride
		if err := rows.Scan(&o.ID, &o.ConfigID, &o.NodeID, &o.Key, &o.Value); err != nil {
			return nil, err
		}
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}

// UpsertOverride creates or replaces a per-node key=value override.
func (q *ConfigQuerier) UpsertOverride(ctx context.Context, configID, nodeID uuid.UUID, key, value string) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO netbox_config_overrides (config_id, node_id, key, value)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (config_id, node_id, key)
		DO UPDATE SET value = EXCLUDED.value`,
		configID, nodeID, key, value)
	return err
}

// DeleteOverride removes a single override.
func (q *ConfigQuerier) DeleteOverride(ctx context.Context, configID, nodeID uuid.UUID, key string) error {
	tag, err := q.pool.Exec(ctx, `
		DELETE FROM netbox_config_overrides
		WHERE config_id = $1 AND node_id = $2 AND key = $3`,
		configID, nodeID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("override not found")
	}
	return nil
}

func scanConfig(row pgx.Row) (*NetboxConfig, error) {
	var c NetboxConfig
	err := row.Scan(&c.ID, &c.ClusterID, &c.Version, &c.ConfigTemplate,
		&c.RenderedHash, &c.PushedAt, &c.PushStatus, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}
