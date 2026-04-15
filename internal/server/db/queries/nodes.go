package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Node represents a row in the nodes table.
type Node struct {
	ID                uuid.UUID
	ClusterID         uuid.UUID
	Hostname          string
	IPAddress         string
	Role              string // "hyperconverged" | "app" | "db_only"
	FailoverPriority  int
	AgentStatus       string // "connected" | "disconnected" | "unknown"
	LastSeenAt        *time.Time
	PatroniState      json.RawMessage
	NetboxRunning     *bool
	RQRunning         *bool
	SuppressAutoStart bool
	SSHPort           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// AgentToken represents a row in the agent_tokens table.
type AgentToken struct {
	ID         uuid.UUID
	NodeID     uuid.UUID
	TokenHash  string
	IssuedAt   time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// RegistrationToken represents a row in the registration_tokens table.
type RegistrationToken struct {
	ID        uuid.UUID
	NodeID    uuid.UUID
	TokenHash string
	IssuedAt  time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// NodeQuerier performs node-related DB operations.
type NodeQuerier struct {
	pool *pgxpool.Pool
}

func NewNodeQuerier(pool *pgxpool.Pool) *NodeQuerier {
	return &NodeQuerier{pool: pool}
}

const nodeColumns = `
	id, cluster_id, hostname, ip_address::text, role, failover_priority,
	agent_status, last_seen_at, patroni_state, netbox_running, rq_running,
	suppress_auto_start, ssh_port, created_at, updated_at`

func scanNode(row interface {
	Scan(...any) error
}) (*Node, error) {
	var n Node
	if err := row.Scan(
		&n.ID, &n.ClusterID, &n.Hostname, &n.IPAddress, &n.Role,
		&n.FailoverPriority, &n.AgentStatus, &n.LastSeenAt,
		&n.PatroniState, &n.NetboxRunning, &n.RQRunning,
		&n.SuppressAutoStart, &n.SSHPort,
		&n.CreatedAt, &n.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &n, nil
}

func (q *NodeQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID) ([]Node, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT`+nodeColumns+` FROM nodes WHERE cluster_id = $1 ORDER BY failover_priority, hostname`,
		clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *n)
	}
	return nodes, rows.Err()
}

func (q *NodeQuerier) GetByID(ctx context.Context, id uuid.UUID) (*Node, error) {
	row := q.pool.QueryRow(ctx,
		`SELECT`+nodeColumns+` FROM nodes WHERE id = $1`, id)
	return scanNode(row)
}

type CreateNodeParams struct {
	ClusterID        uuid.UUID
	Hostname         string
	IPAddress        string
	Role             string
	FailoverPriority int
	SSHPort          int
}

func (q *NodeQuerier) Create(ctx context.Context, p CreateNodeParams) (*Node, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO nodes (cluster_id, hostname, ip_address, role, failover_priority, ssh_port)
		VALUES ($1, $2, $3::inet, $4, $5, $6)
		RETURNING`+nodeColumns,
		p.ClusterID, p.Hostname, p.IPAddress, p.Role, p.FailoverPriority, p.SSHPort,
	)
	return scanNode(row)
}

func (q *NodeQuerier) UpdateAgentStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE nodes SET agent_status = $2, last_seen_at = now(), updated_at = now()
		WHERE id = $1
	`, id, status)
	return err
}

func (q *NodeQuerier) UpdateHeartbeat(ctx context.Context, id uuid.UUID, netboxRunning, rqRunning bool, patroniState json.RawMessage) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE nodes
		SET last_seen_at = now(),
		    agent_status = 'connected',
		    netbox_running = $2,
		    rq_running = $3,
		    patroni_state = $4,
		    updated_at = now()
		WHERE id = $1
	`, id, netboxRunning, rqRunning, patroniState)
	return err
}

func (q *NodeQuerier) UpdatePriority(ctx context.Context, id uuid.UUID, priority int) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE nodes SET failover_priority = $2, updated_at = now() WHERE id = $1
	`, id, priority)
	return err
}

func (q *NodeQuerier) SetSuppressAutoStart(ctx context.Context, id uuid.UUID, suppress bool) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE nodes SET suppress_auto_start = $2, updated_at = now() WHERE id = $1
	`, id, suppress)
	return err
}

func (q *NodeQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, id)
	return err
}

// ── Agent tokens ──────────────────────────────────────────────────────────────

// AgentTokenQuerier handles agent token operations.
type AgentTokenQuerier struct {
	pool *pgxpool.Pool
}

func NewAgentTokenQuerier(pool *pgxpool.Pool) *AgentTokenQuerier {
	return &AgentTokenQuerier{pool: pool}
}

func (q *AgentTokenQuerier) Create(ctx context.Context, nodeID uuid.UUID, tokenHash string) error {
	_, err := q.pool.Exec(ctx,
		`INSERT INTO agent_tokens (node_id, token_hash) VALUES ($1, $2)`,
		nodeID, tokenHash)
	return err
}

func (q *AgentTokenQuerier) GetValid(ctx context.Context, tokenHash string) (*AgentToken, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, node_id, token_hash, issued_at, revoked_at, last_used_at
		FROM agent_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash)

	var t AgentToken
	if err := row.Scan(
		&t.ID, &t.NodeID, &t.TokenHash,
		&t.IssuedAt, &t.RevokedAt, &t.LastUsedAt,
	); err != nil {
		return nil, err
	}
	return &t, nil
}

func (q *AgentTokenQuerier) Touch(ctx context.Context, tokenHash string) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE agent_tokens SET last_used_at = now() WHERE token_hash = $1`, tokenHash)
	return err
}

func (q *AgentTokenQuerier) Revoke(ctx context.Context, nodeID uuid.UUID) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE agent_tokens SET revoked_at = now() WHERE node_id = $1 AND revoked_at IS NULL`, nodeID)
	return err
}

// ── Registration tokens ───────────────────────────────────────────────────────

// RegistrationTokenQuerier handles one-time registration token operations.
type RegistrationTokenQuerier struct {
	pool *pgxpool.Pool
}

func NewRegistrationTokenQuerier(pool *pgxpool.Pool) *RegistrationTokenQuerier {
	return &RegistrationTokenQuerier{pool: pool}
}

func (q *RegistrationTokenQuerier) Create(ctx context.Context, nodeID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	_, err := q.pool.Exec(ctx,
		`INSERT INTO registration_tokens (node_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		nodeID, tokenHash, expiresAt)
	return err
}

func (q *RegistrationTokenQuerier) Consume(ctx context.Context, tokenHash string) (*RegistrationToken, error) {
	row := q.pool.QueryRow(ctx, `
		UPDATE registration_tokens
		SET used_at = now()
		WHERE token_hash = $1
		  AND used_at IS NULL
		  AND expires_at > now()
		RETURNING id, node_id, token_hash, issued_at, expires_at, used_at
	`, tokenHash)

	var rt RegistrationToken
	if err := row.Scan(
		&rt.ID, &rt.NodeID, &rt.TokenHash,
		&rt.IssuedAt, &rt.ExpiresAt, &rt.UsedAt,
	); err != nil {
		return nil, err
	}
	return &rt, nil
}
