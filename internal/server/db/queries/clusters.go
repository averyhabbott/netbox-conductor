package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cluster represents a row in the clusters table.
type Cluster struct {
	ID               uuid.UUID
	Name             string
	Description      string
	Mode             string // "active_standby" | "ha"
	AutoFailover     bool
	AutoFailback     bool
	AppTierAlwaysAvailable bool
	FailoverOnMaintenance  bool
	FailoverDelaySecs      int
	FailbackMultiplier     int
	VIP              *string
	PatroniScope     string
	NetboxVersion    string
	NetboxSecretKey  []byte // encrypted
	APITokenPepper   []byte // encrypted
	// Media sync
	MediaSyncEnabled        bool
	ExtraFoldersSyncEnabled bool
	ExtraSyncFolders        []string
	// Patroni / HA
	PatroniConfigured    bool
	RedisSentinelMaster  string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ClusterQuerier performs cluster-related DB operations.
type ClusterQuerier struct {
	pool *pgxpool.Pool
}

func NewClusterQuerier(pool *pgxpool.Pool) *ClusterQuerier {
	return &ClusterQuerier{pool: pool}
}

const clusterColumns = `
	id, name, description, mode, auto_failover, auto_failback,
	app_tier_always_available, failover_on_maintenance, failover_delay_secs, failback_multiplier,
	vip::text, patroni_scope,
	netbox_version, netbox_secret_key, api_token_pepper,
	media_sync_enabled, extra_folders_sync_enabled, extra_sync_folders,
	patroni_configured, redis_sentinel_master,
	created_at, updated_at`

func scanCluster(row interface {
	Scan(...any) error
}) (*Cluster, error) {
	var c Cluster
	if err := row.Scan(
		&c.ID, &c.Name, &c.Description, &c.Mode, &c.AutoFailover, &c.AutoFailback,
		&c.AppTierAlwaysAvailable, &c.FailoverOnMaintenance, &c.FailoverDelaySecs, &c.FailbackMultiplier,
		&c.VIP, &c.PatroniScope, &c.NetboxVersion,
		&c.NetboxSecretKey, &c.APITokenPepper,
		&c.MediaSyncEnabled, &c.ExtraFoldersSyncEnabled, &c.ExtraSyncFolders,
		&c.PatroniConfigured, &c.RedisSentinelMaster,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

func (q *ClusterQuerier) List(ctx context.Context) ([]Cluster, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT`+clusterColumns+` FROM clusters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clusters []Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, *c)
	}
	return clusters, rows.Err()
}

func (q *ClusterQuerier) GetByID(ctx context.Context, id uuid.UUID) (*Cluster, error) {
	row := q.pool.QueryRow(ctx,
		`SELECT`+clusterColumns+` FROM clusters WHERE id = $1`, id)
	return scanCluster(row)
}

type CreateClusterParams struct {
	Name            string
	Description     string
	Mode            string
	PatroniScope    string
	NetboxVersion   string
	NetboxSecretKey []byte
	APITokenPepper  []byte
}

func (q *ClusterQuerier) Create(ctx context.Context, p CreateClusterParams) (*Cluster, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO clusters
			(name, description, mode, patroni_scope, netbox_version, netbox_secret_key, api_token_pepper)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING`+clusterColumns,
		p.Name, p.Description, p.Mode, p.PatroniScope, p.NetboxVersion,
		p.NetboxSecretKey, p.APITokenPepper,
	)
	return scanCluster(row)
}

type UpdateClusterParams struct {
	ID                     uuid.UUID
	AutoFailover           bool
	AutoFailback           bool
	AppTierAlwaysAvailable bool
	FailoverOnMaintenance  bool
	FailoverDelaySecs      int
	FailbackMultiplier     int
	VIP                    *string
	RedisSentinelMaster    string
}

func (q *ClusterQuerier) UpdateFailoverSettings(ctx context.Context, p UpdateClusterParams) error {
	sentinel := p.RedisSentinelMaster
	if sentinel == "" {
		sentinel = "netbox"
	}
	_, err := q.pool.Exec(ctx, `
		UPDATE clusters
		SET auto_failover             = $2,
		    auto_failback             = $3,
		    app_tier_always_available = $4,
		    failover_on_maintenance   = $5,
		    failover_delay_secs       = $6,
		    failback_multiplier       = $7,
		    vip                       = $8::inet,
		    redis_sentinel_master     = $9,
		    updated_at                = now()
		WHERE id = $1
	`, p.ID, p.AutoFailover, p.AutoFailback,
		p.AppTierAlwaysAvailable, p.FailoverOnMaintenance, p.FailoverDelaySecs,
		p.FailbackMultiplier, p.VIP, sentinel)
	return err
}

// SetPatroniConfigured marks a cluster as having had Patroni fully configured.
// Called at the end of a successful Configure Failover operation.
func (q *ClusterQuerier) SetPatroniConfigured(ctx context.Context, clusterID uuid.UUID) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE clusters SET patroni_configured = TRUE, updated_at = now() WHERE id = $1`,
		clusterID)
	return err
}

type UpdateMediaSyncParams struct {
	ID                      uuid.UUID
	MediaSyncEnabled        bool
	ExtraFoldersSyncEnabled bool
	ExtraSyncFolders        []string
}

func (q *ClusterQuerier) UpdateMediaSyncSettings(ctx context.Context, p UpdateMediaSyncParams) error {
	folders := p.ExtraSyncFolders
	if folders == nil {
		folders = []string{}
	}
	_, err := q.pool.Exec(ctx, `
		UPDATE clusters
		SET media_sync_enabled = $2, extra_folders_sync_enabled = $3, extra_sync_folders = $4, updated_at = now()
		WHERE id = $1
	`, p.ID, p.MediaSyncEnabled, p.ExtraFoldersSyncEnabled, folders)
	return err
}

// UpdateNetboxVersion sets the cluster's netbox_version to the value reported
// by an agent heartbeat. Called at most once per unique version change.
func (q *ClusterQuerier) UpdateNetboxVersion(ctx context.Context, clusterID uuid.UUID, version string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE clusters SET netbox_version = $2, updated_at = now()
		WHERE id = $1 AND netbox_version != $2
	`, clusterID, version)
	return err
}

func (q *ClusterQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM clusters WHERE id = $1`, id)
	return err
}

func (q *ClusterQuerier) CountClusters(ctx context.Context) (int, error) {
	var n int
	err := q.pool.QueryRow(ctx, `SELECT COUNT(*) FROM clusters`).Scan(&n)
	return n, err
}
