package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PatroniDesign holds the conductor-managed Patroni DCS config for a cluster.
type PatroniDesign struct {
	ClusterID uuid.UUID
	Config    json.RawMessage
	UpdatedAt time.Time
}

type PatroniDesignQuerier struct {
	pool *pgxpool.Pool
}

func NewPatroniDesignQuerier(pool *pgxpool.Pool) *PatroniDesignQuerier {
	return &PatroniDesignQuerier{pool: pool}
}

// Upsert deep-merges fields into the stored design config.
// If no row exists for the cluster it is created. Nested objects are merged
// recursively so that configure-failover and configure-backups each accumulate
// their own keys without overwriting the other's subtrees.
func (q *PatroniDesignQuerier) Upsert(ctx context.Context, clusterID uuid.UUID, fields json.RawMessage) error {
	var newFields map[string]any
	if err := json.Unmarshal(fields, &newFields); err != nil {
		return err
	}

	existing, err := q.Get(ctx, clusterID)
	var base map[string]any
	if err != nil {
		base = make(map[string]any)
	} else {
		if err := json.Unmarshal(existing.Config, &base); err != nil {
			base = make(map[string]any)
		}
	}

	merged := deepMerge(base, newFields)
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return err
	}

	_, err = q.pool.Exec(ctx, `
		INSERT INTO cluster_patroni_designs (cluster_id, config, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (cluster_id) DO UPDATE
		  SET config     = EXCLUDED.config,
		      updated_at = now()
	`, clusterID, mergedJSON)
	return err
}

// deepMerge recursively merges src into dst. For keys present in both where
// both values are objects, the merge recurses. Otherwise src wins.
func deepMerge(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst))
	for k, v := range dst {
		out[k] = v
	}
	for k, sv := range src {
		if dv, ok := out[k]; ok {
			dMap, dIsMap := dv.(map[string]any)
			sMap, sIsMap := sv.(map[string]any)
			if dIsMap && sIsMap {
				out[k] = deepMerge(dMap, sMap)
				continue
			}
		}
		out[k] = sv
	}
	return out
}

// Get returns the stored design config for a cluster, or nil if none exists.
func (q *PatroniDesignQuerier) Get(ctx context.Context, clusterID uuid.UUID) (*PatroniDesign, error) {
	var d PatroniDesign
	err := q.pool.QueryRow(ctx, `
		SELECT cluster_id, config, updated_at
		FROM cluster_patroni_designs
		WHERE cluster_id = $1
	`, clusterID).Scan(&d.ClusterID, &d.Config, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
