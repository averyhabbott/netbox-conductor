package queries

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Heartbeat represents one row in node_heartbeats.
type Heartbeat struct {
	ID                   uuid.UUID `json:"id"`
	NodeID               uuid.UUID `json:"node_id"`
	ClusterID            uuid.UUID `json:"cluster_id"`
	LoadAvg1             *float64  `json:"load_avg_1,omitempty"`
	LoadAvg5             *float64  `json:"load_avg_5,omitempty"`
	MemUsedPct           *float64  `json:"mem_used_pct,omitempty"`
	DiskUsedPct          *float64  `json:"disk_used_pct,omitempty"`
	NetboxRunning        *bool     `json:"netbox_running,omitempty"`
	RQRunning            *bool     `json:"rq_running,omitempty"`
	RedisRunning         *bool     `json:"redis_running,omitempty"`
	SentinelRunning      *bool     `json:"sentinel_running,omitempty"`
	PatroniRunning       *bool     `json:"patroni_running,omitempty"`
	PostgresRunning      *bool     `json:"postgres_running,omitempty"`
	PatroniRole          *string   `json:"patroni_role,omitempty"`
	RedisRole            *string   `json:"redis_role,omitempty"`
	ReplicationLagBytes  *int64    `json:"replication_lag_bytes,omitempty"`
	RecordedAt           time.Time `json:"recorded_at"`
}

// InsertHeartbeatParams holds fields for inserting a heartbeat row.
type InsertHeartbeatParams struct {
	NodeID              uuid.UUID
	ClusterID           uuid.UUID
	LoadAvg1            *float64
	LoadAvg5            *float64
	MemUsedPct          *float64
	DiskUsedPct         *float64
	NetboxRunning       *bool
	RQRunning           *bool
	RedisRunning        *bool
	SentinelRunning     *bool
	PatroniRunning      *bool
	PostgresRunning     *bool
	PatroniRole         *string
	RedisRole           *string
	ReplicationLagBytes *int64
}

// HeartbeatQuerier performs node_heartbeats operations.
type HeartbeatQuerier struct {
	pool *pgxpool.Pool
}

func NewHeartbeatQuerier(pool *pgxpool.Pool) *HeartbeatQuerier {
	return &HeartbeatQuerier{pool: pool}
}

// Insert writes a heartbeat row.
func (q *HeartbeatQuerier) Insert(ctx context.Context, p InsertHeartbeatParams) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO node_heartbeats
			(node_id, cluster_id,
			 load_avg_1, load_avg_5, mem_used_pct, disk_used_pct,
			 netbox_running, rq_running, redis_running, sentinel_running,
			 patroni_running, postgres_running,
			 patroni_role, redis_role, replication_lag_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		p.NodeID, p.ClusterID,
		p.LoadAvg1, p.LoadAvg5, p.MemUsedPct, p.DiskUsedPct,
		p.NetboxRunning, p.RQRunning, p.RedisRunning, p.SentinelRunning,
		p.PatroniRunning, p.PostgresRunning,
		p.PatroniRole, p.RedisRole, p.ReplicationLagBytes,
	)
	return err
}

// List returns heartbeats for a node in descending time order.
func (q *HeartbeatQuerier) List(ctx context.Context, nodeID uuid.UUID, from, to *time.Time, limit int) ([]Heartbeat, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}

	var conds []string
	var args []interface{}

	args = append(args, nodeID)
	conds = append(conds, "node_id = $1")

	if from != nil {
		args = append(args, *from)
		conds = append(conds, fmt.Sprintf("recorded_at >= $%d", len(args)))
	}
	if to != nil {
		args = append(args, *to)
		conds = append(conds, fmt.Sprintf("recorded_at <= $%d", len(args)))
	}

	args = append(args, limit)
	where := "WHERE " + strings.Join(conds, " AND ")
	sql := fmt.Sprintf(`
		SELECT id, node_id, cluster_id,
		       load_avg_1, load_avg_5, mem_used_pct, disk_used_pct,
		       netbox_running, rq_running, redis_running, sentinel_running,
		       patroni_running, postgres_running,
		       patroni_role, redis_role, replication_lag_bytes, recorded_at
		FROM node_heartbeats
		%s
		ORDER BY recorded_at DESC
		LIMIT $%d`, where, len(args))

	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Heartbeat
	for rows.Next() {
		var h Heartbeat
		if err := rows.Scan(
			&h.ID, &h.NodeID, &h.ClusterID,
			&h.LoadAvg1, &h.LoadAvg5, &h.MemUsedPct, &h.DiskUsedPct,
			&h.NetboxRunning, &h.RQRunning, &h.RedisRunning, &h.SentinelRunning,
			&h.PatroniRunning, &h.PostgresRunning,
			&h.PatroniRole, &h.RedisRole, &h.ReplicationLagBytes, &h.RecordedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, h)
	}
	return result, rows.Err()
}

// LastByNode returns the most recent heartbeat for a node, or nil if none.
func (q *HeartbeatQuerier) LastByNode(ctx context.Context, nodeID uuid.UUID) (*Heartbeat, error) {
	var h Heartbeat
	err := q.pool.QueryRow(ctx, `
		SELECT id, node_id, cluster_id,
		       load_avg_1, load_avg_5, mem_used_pct, disk_used_pct,
		       netbox_running, rq_running, redis_running, sentinel_running,
		       patroni_running, postgres_running,
		       patroni_role, redis_role, replication_lag_bytes, recorded_at
		FROM node_heartbeats
		WHERE node_id = $1
		ORDER BY recorded_at DESC
		LIMIT 1`, nodeID,
	).Scan(
		&h.ID, &h.NodeID, &h.ClusterID,
		&h.LoadAvg1, &h.LoadAvg5, &h.MemUsedPct, &h.DiskUsedPct,
		&h.NetboxRunning, &h.RQRunning, &h.RedisRunning, &h.SentinelRunning,
		&h.PatroniRunning, &h.PostgresRunning,
		&h.PatroniRole, &h.RedisRole, &h.ReplicationLagBytes, &h.RecordedAt,
	)
	if err != nil {
		return nil, err
	}
	return &h, nil
}
