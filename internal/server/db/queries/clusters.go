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
	Mode             string // "active_standby" | "ha"
	AutoFailover     bool
	AutoFailback     bool
	VIP              *string
	PatroniScope     string
	NetboxVersion    string
	NetboxSecretKey  []byte // encrypted
	APITokenPepper   []byte // encrypted
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
	id, name, mode, auto_failover, auto_failback, vip::text, patroni_scope,
	netbox_version, netbox_secret_key, api_token_pepper, created_at, updated_at`

func scanCluster(row interface {
	Scan(...any) error
}) (*Cluster, error) {
	var c Cluster
	if err := row.Scan(
		&c.ID, &c.Name, &c.Mode, &c.AutoFailover, &c.AutoFailback,
		&c.VIP, &c.PatroniScope, &c.NetboxVersion,
		&c.NetboxSecretKey, &c.APITokenPepper,
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
	Mode            string
	PatroniScope    string
	NetboxVersion   string
	NetboxSecretKey []byte
	APITokenPepper  []byte
}

func (q *ClusterQuerier) Create(ctx context.Context, p CreateClusterParams) (*Cluster, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO clusters
			(name, mode, patroni_scope, netbox_version, netbox_secret_key, api_token_pepper)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING`+clusterColumns,
		p.Name, p.Mode, p.PatroniScope, p.NetboxVersion,
		p.NetboxSecretKey, p.APITokenPepper,
	)
	return scanCluster(row)
}

type UpdateClusterParams struct {
	ID           uuid.UUID
	AutoFailover bool
	AutoFailback bool
	VIP          *string
}

func (q *ClusterQuerier) UpdateFailoverSettings(ctx context.Context, p UpdateClusterParams) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE clusters
		SET auto_failover = $2, auto_failback = $3, vip = $4::inet, updated_at = now()
		WHERE id = $1
	`, p.ID, p.AutoFailover, p.AutoFailback, p.VIP)
	return err
}

func (q *ClusterQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM clusters WHERE id = $1`, id)
	return err
}
