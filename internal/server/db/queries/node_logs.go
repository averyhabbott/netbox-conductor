package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NodeLogEntry represents a structured log line stored for a cluster node.
type NodeLogEntry struct {
	ID         uuid.UUID  `json:"id"`
	ClusterID  uuid.UUID  `json:"cluster_id"`
	NodeID     *uuid.UUID `json:"node_id,omitempty"`
	Hostname   string     `json:"hostname"`
	Level      string     `json:"level"`
	Source     string     `json:"source"`
	Message    string     `json:"message"`
	LogFile    *string    `json:"log_file,omitempty"`
	OccurredAt time.Time  `json:"occurred_at"`
}

// InsertNodeLogParams holds the fields for inserting a new log entry.
type InsertNodeLogParams struct {
	ClusterID uuid.UUID
	NodeID    *uuid.UUID
	Hostname  string
	Level     string  // debug | info | warn | error
	Source    string  // conductor | agent | netbox
	Message   string
	LogFile   *string
}

// NodeLogQuerier performs node_log_entries operations.
type NodeLogQuerier struct {
	pool *pgxpool.Pool
}

func NewNodeLogQuerier(pool *pgxpool.Pool) *NodeLogQuerier {
	return &NodeLogQuerier{pool: pool}
}

// Insert writes a single log entry.
func (q *NodeLogQuerier) Insert(ctx context.Context, p InsertNodeLogParams) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO node_log_entries
			(cluster_id, node_id, hostname, level, source, message, log_file)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.ClusterID, p.NodeID, p.Hostname, p.Level, p.Source, p.Message, p.LogFile,
	)
	return err
}

// ListByCluster returns log entries for a cluster, optionally filtered by minimum
// level (debug < info < warn < error). Pass "" to include all levels.
// Results are ordered newest-first, capped at limit (max 500).
func (q *NodeLogQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID, minLevel string, limit int) ([]NodeLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	// Map level string to numeric priority for filtering.
	var levelFilter int
	switch minLevel {
	case "warn":
		levelFilter = 2
	case "error":
		levelFilter = 3
	case "debug":
		levelFilter = 0
	default: // info or ""
		levelFilter = 1
	}

	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, node_id, hostname, level, source, message, log_file, occurred_at
		FROM node_log_entries
		WHERE cluster_id = $1
		  AND CASE level
		        WHEN 'debug' THEN 0
		        WHEN 'info'  THEN 1
		        WHEN 'warn'  THEN 2
		        WHEN 'error' THEN 3
		        ELSE 1
		      END >= $2
		ORDER BY occurred_at DESC
		LIMIT $3`,
		clusterID, levelFilter, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []NodeLogEntry
	for rows.Next() {
		var e NodeLogEntry
		if err := rows.Scan(
			&e.ID, &e.ClusterID, &e.NodeID, &e.Hostname,
			&e.Level, &e.Source, &e.Message, &e.LogFile, &e.OccurredAt,
		); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
