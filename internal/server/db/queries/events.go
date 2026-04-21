package queries

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// EventQuerier performs events table operations.
type EventQuerier struct {
	pool *pgxpool.Pool
}

func NewEventQuerier(pool *pgxpool.Pool) *EventQuerier {
	return &EventQuerier{pool: pool}
}

// Insert persists a single event.  Implements events.Store.
func (q *EventQuerier) Insert(ctx context.Context, e events.Event) error {
	var metaJSON []byte
	if e.Metadata != nil {
		metaJSON, _ = json.Marshal(e.Metadata)
	}
	_, err := q.pool.Exec(ctx, `
		INSERT INTO events
			(id, cluster_id, node_id, category, severity, code, message, actor, metadata, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID, e.ClusterID, e.NodeID,
		e.Category, e.Severity, e.Code, e.Message, e.Actor,
		metaJSON, e.OccurredAt,
	)
	return err
}

// EventFilter specifies query constraints for List.
type EventFilter struct {
	Category  string
	// Code is matched as a prefix when it does not end with a digit
	// (e.g. "NBC-HA" matches all HA events) or exactly otherwise.
	Code      string
	Severity  string     // minimum severity label
	ClusterID *uuid.UUID
	NodeID    *uuid.UUID
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

// List returns events matching the filter, newest-first.
func (q *EventQuerier) List(ctx context.Context, f EventFilter) ([]events.Event, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}

	var conds []string
	var args []interface{}

	add := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if f.Category != "" {
		conds = append(conds, "category = "+add(f.Category))
	}
	if f.Code != "" {
		// Prefix match if code has no digit suffix (NBC-HA → NBC-HA-%)
		last := f.Code[len(f.Code)-1]
		if last < '0' || last > '9' {
			conds = append(conds, "code LIKE "+add(strings.TrimRight(f.Code, "-")+"%"))
		} else {
			conds = append(conds, "code = "+add(f.Code))
		}
	}
	if f.Severity != "" {
		ranks := map[string]int{
			"debug": 0, "info": 1, "warn": 2, "error": 3, "critical": 4,
		}
		rank := ranks[f.Severity]
		conds = append(conds, fmt.Sprintf(
			`CASE severity WHEN 'debug' THEN 0 WHEN 'info' THEN 1 WHEN 'warn' THEN 2 WHEN 'error' THEN 3 WHEN 'critical' THEN 4 ELSE 1 END >= %s`,
			add(rank)))
	}
	if f.ClusterID != nil {
		conds = append(conds, "cluster_id = "+add(*f.ClusterID))
	}
	if f.NodeID != nil {
		conds = append(conds, "node_id = "+add(*f.NodeID))
	}
	if f.From != nil {
		conds = append(conds, "occurred_at >= "+add(*f.From))
	}
	if f.To != nil {
		conds = append(conds, "occurred_at <= "+add(*f.To))
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	limitP := add(f.Limit)
	offsetP := add(f.Offset)

	// Prefix conditions with "ev." since events is aliased below.
	for i, c := range conds {
		// The conditions reference bare column names; qualify them for the aliased query.
		conds[i] = strings.NewReplacer(
			"category =", "ev.category =",
			"code LIKE", "ev.code LIKE",
			"code =", "ev.code =",
			"CASE severity", "CASE ev.severity",
			"cluster_id =", "ev.cluster_id =",
			"node_id =", "ev.node_id =",
			"occurred_at >=", "ev.occurred_at >=",
			"occurred_at <=", "ev.occurred_at <=",
		).Replace(c)
		_ = i
	}

	where = ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	sql := fmt.Sprintf(`
		SELECT ev.id, ev.cluster_id, ev.node_id,
		       c.name  AS cluster_name,
		       n.hostname AS node_name,
		       ev.category, ev.severity, ev.code, ev.message, ev.actor, ev.metadata, ev.occurred_at
		FROM events ev
		LEFT JOIN clusters c ON c.id = ev.cluster_id
		LEFT JOIN nodes    n ON n.id  = ev.node_id
		%s
		ORDER BY ev.occurred_at DESC
		LIMIT %s OFFSET %s`, where, limitP, offsetP)

	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []events.Event
	for rows.Next() {
		var e events.Event
		var metaJSON []byte
		if err := rows.Scan(
			&e.ID, &e.ClusterID, &e.NodeID,
			&e.ClusterName, &e.NodeName,
			&e.Category, &e.Severity, &e.Code, &e.Message, &e.Actor,
			&metaJSON, &e.OccurredAt,
		); err != nil {
			return nil, err
		}
		if metaJSON != nil {
			_ = json.Unmarshal(metaJSON, &e.Metadata)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
