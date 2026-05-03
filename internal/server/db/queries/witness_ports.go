package queries

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WitnessPortQuerier struct {
	pool *pgxpool.Pool
}

func NewWitnessPortQuerier(pool *pgxpool.Pool) *WitnessPortQuerier {
	return &WitnessPortQuerier{pool: pool}
}

// GetPort returns the stored port for a cluster, or 0 if not found.
func (q *WitnessPortQuerier) GetPort(ctx context.Context, clusterID uuid.UUID) (int, error) {
	var port int
	err := q.pool.QueryRow(ctx, `
		SELECT port FROM witness_ports WHERE cluster_id = $1
	`, clusterID).Scan(&port)
	if err != nil {
		return 0, err
	}
	return port, nil
}

// ListPorts returns all currently allocated witness ports.
func (q *WitnessPortQuerier) ListPorts(ctx context.Context) ([]int, error) {
	rows, err := q.pool.Query(ctx, `SELECT port FROM witness_ports`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ports []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, rows.Err()
}

// AllocatePort stores the port assignment for a cluster.
func (q *WitnessPortQuerier) AllocatePort(ctx context.Context, clusterID uuid.UUID, port int) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO witness_ports (cluster_id, port) VALUES ($1, $2)
		ON CONFLICT (cluster_id) DO UPDATE SET port = EXCLUDED.port
	`, clusterID, port)
	return err
}
