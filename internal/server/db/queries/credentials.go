package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Credential represents a row in the credentials table (password_enc is never returned to callers).
type Credential struct {
	ID          uuid.UUID
	ClusterID   uuid.UUID
	Kind        string // "postgres_superuser" | "postgres_replication" | "netbox_db_user" | "redis_tasks_password" | "redis_caching_password" | "netbox_secret_key" | "netbox_api_token_pepper" | "patroni_rest_password"
	Username    string
	PasswordEnc []byte
	DBName      *string
	CreatedAt   time.Time
	RotatedAt   *time.Time
}

// CredentialQuerier handles credential DB operations.
type CredentialQuerier struct {
	pool *pgxpool.Pool
}

func NewCredentialQuerier(pool *pgxpool.Pool) *CredentialQuerier {
	return &CredentialQuerier{pool: pool}
}

func (q *CredentialQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID) ([]Credential, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, kind, username, password_enc, db_name, created_at, rotated_at
		FROM credentials
		WHERE cluster_id = $1
		ORDER BY kind
	`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(
			&c.ID, &c.ClusterID, &c.Kind, &c.Username, &c.PasswordEnc,
			&c.DBName, &c.CreatedAt, &c.RotatedAt,
		); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

func (q *CredentialQuerier) GetByKind(ctx context.Context, clusterID uuid.UUID, kind string) (*Credential, error) {
	var c Credential
	err := q.pool.QueryRow(ctx, `
		SELECT id, cluster_id, kind, username, password_enc, db_name, created_at, rotated_at
		FROM credentials
		WHERE cluster_id = $1 AND kind = $2
	`, clusterID, kind).Scan(
		&c.ID, &c.ClusterID, &c.Kind, &c.Username, &c.PasswordEnc,
		&c.DBName, &c.CreatedAt, &c.RotatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

type UpsertCredentialParams struct {
	ClusterID   uuid.UUID
	Kind        string
	Username    string
	PasswordEnc []byte
	DBName      *string
}

func (q *CredentialQuerier) Upsert(ctx context.Context, p UpsertCredentialParams) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO credentials (cluster_id, kind, username, password_enc, db_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cluster_id, kind) DO UPDATE
		SET username = EXCLUDED.username,
		    password_enc = EXCLUDED.password_enc,
		    db_name = EXCLUDED.db_name,
		    rotated_at = now()
	`, p.ClusterID, p.Kind, p.Username, p.PasswordEnc, p.DBName)
	return err
}
