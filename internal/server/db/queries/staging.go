package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Staging tokens ─────────────────────────────────────────────────────────────

// StagingToken represents a row in staging_tokens.
type StagingToken struct {
	ID        uuid.UUID
	TokenHash string
	Label     string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// StagingTokenQuerier performs staging token operations.
type StagingTokenQuerier struct {
	pool *pgxpool.Pool
}

func NewStagingTokenQuerier(pool *pgxpool.Pool) *StagingTokenQuerier {
	return &StagingTokenQuerier{pool: pool}
}

func (q *StagingTokenQuerier) Create(ctx context.Context, tokenHash, label string, expiresAt time.Time) (*StagingToken, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO staging_tokens (token_hash, label, expires_at)
		VALUES ($1, $2, $3)
		RETURNING id, token_hash, label, created_at, expires_at, used_at
	`, tokenHash, label, expiresAt)

	var t StagingToken
	if err := row.Scan(&t.ID, &t.TokenHash, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (q *StagingTokenQuerier) List(ctx context.Context) ([]StagingToken, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, token_hash, label, created_at, expires_at, used_at
		FROM staging_tokens
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []StagingToken
	for rows.Next() {
		var t StagingToken
		if err := rows.Scan(&t.ID, &t.TokenHash, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// Consume atomically marks the staging token as used and returns it.
// Returns an error if the token doesn't exist, is already used, or is expired.
func (q *StagingTokenQuerier) Consume(ctx context.Context, tokenHash string) (*StagingToken, error) {
	row := q.pool.QueryRow(ctx, `
		UPDATE staging_tokens
		SET used_at = now()
		WHERE token_hash = $1
		  AND used_at IS NULL
		  AND expires_at > now()
		RETURNING id, token_hash, label, created_at, expires_at, used_at
	`, tokenHash)

	var t StagingToken
	if err := row.Scan(&t.ID, &t.TokenHash, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (q *StagingTokenQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM staging_tokens WHERE id = $1`, id)
	return err
}

// ── Staging agents ─────────────────────────────────────────────────────────────

// StagingAgent represents a row in staging_agents.
type StagingAgent struct {
	ID           uuid.UUID
	Hostname     string
	IPAddress    string
	OS           string
	Arch         string
	AgentVersion string
	TokenHash    string
	Status       string
	LastSeenAt   *time.Time
	CreatedAt    time.Time
}

// StagingAgentQuerier performs staging agent operations.
type StagingAgentQuerier struct {
	pool *pgxpool.Pool
}

func NewStagingAgentQuerier(pool *pgxpool.Pool) *StagingAgentQuerier {
	return &StagingAgentQuerier{pool: pool}
}

const stagingAgentColumns = `id, hostname, ip_address, os, arch, agent_version, token_hash, status, last_seen_at, created_at`

func scanStagingAgent(row interface{ Scan(...any) error }) (*StagingAgent, error) {
	var a StagingAgent
	if err := row.Scan(
		&a.ID, &a.Hostname, &a.IPAddress, &a.OS, &a.Arch,
		&a.AgentVersion, &a.TokenHash, &a.Status, &a.LastSeenAt, &a.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &a, nil
}

type CreateStagingAgentParams struct {
	Hostname     string
	IPAddress    string
	OS           string
	Arch         string
	AgentVersion string
	TokenHash    string
}

func (q *StagingAgentQuerier) Create(ctx context.Context, p CreateStagingAgentParams) (*StagingAgent, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO staging_agents (hostname, ip_address, os, arch, agent_version, token_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+stagingAgentColumns,
		p.Hostname, p.IPAddress, p.OS, p.Arch, p.AgentVersion, p.TokenHash,
	)
	return scanStagingAgent(row)
}

func (q *StagingAgentQuerier) List(ctx context.Context) ([]StagingAgent, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT `+stagingAgentColumns+` FROM staging_agents ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []StagingAgent
	for rows.Next() {
		a, err := scanStagingAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, *a)
	}
	return agents, rows.Err()
}

func (q *StagingAgentQuerier) GetByID(ctx context.Context, id uuid.UUID) (*StagingAgent, error) {
	row := q.pool.QueryRow(ctx,
		`SELECT `+stagingAgentColumns+` FROM staging_agents WHERE id = $1`, id)
	return scanStagingAgent(row)
}

func (q *StagingAgentQuerier) GetByTokenHash(ctx context.Context, tokenHash string) (*StagingAgent, error) {
	row := q.pool.QueryRow(ctx,
		`SELECT `+stagingAgentColumns+` FROM staging_agents WHERE token_hash = $1`, tokenHash)
	return scanStagingAgent(row)
}

func (q *StagingAgentQuerier) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE staging_agents SET status = $2, last_seen_at = now() WHERE id = $1
	`, id, status)
	return err
}

func (q *StagingAgentQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM staging_agents WHERE id = $1`, id)
	return err
}
