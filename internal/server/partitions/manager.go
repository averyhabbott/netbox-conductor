package partitions

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Manager creates future partitions and drops expired ones for the
// events and node_heartbeats tables. It runs on a daily tick.
//
// Both tables have a _default partition that catches any rows outside
// the pre-created range, so missed ticks are safe — rows land in _default
// and the next successful run creates the proper partition.
type Manager struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Manager {
	return &Manager{pool: pool}
}

// Run starts the partition manager loop. It runs once immediately, then
// every 24 hours until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	m.tick(ctx)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	if err := m.ensureFuturePartitions(ctx); err != nil {
		slog.Warn("partition manager: ensure future partitions failed", "error", err)
	}
	if err := m.dropExpiredPartitions(ctx); err != nil {
		slog.Warn("partition manager: drop expired partitions failed", "error", err)
	}
}

// ensureFuturePartitions creates monthly partitions for the next 3 months
// for both events and node_heartbeats if they don't already exist.
func (m *Manager) ensureFuturePartitions(ctx context.Context) error {
	now := time.Now().UTC()
	horizon := now.AddDate(0, 3, 0)

	tables := []struct{ parent, prefix string }{
		{"events", "events"},
		{"node_heartbeats", "node_heartbeats"},
	}

	for cur := startOfMonth(now); !cur.After(startOfMonth(horizon)); cur = cur.AddDate(0, 1, 0) {
		next := cur.AddDate(0, 1, 0)
		label := cur.Format("2006_01")

		for _, t := range tables {
			partName := fmt.Sprintf("%s_%s", t.prefix, label)
			_, err := m.pool.Exec(ctx, fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				partName, t.parent,
				cur.Format("2006-01-02"),
				next.Format("2006-01-02"),
			))
			if err != nil {
				return fmt.Errorf("create partition %s: %w", partName, err)
			}
		}
	}
	return nil
}

// dropExpiredPartitions detaches and drops monthly partitions older than
// the configured retention period for each category.
func (m *Manager) dropExpiredPartitions(ctx context.Context) error {
	rows, err := m.pool.Query(ctx, `SELECT category, retain_days FROM event_retention`)
	if err != nil {
		return fmt.Errorf("query event_retention: %w", err)
	}
	defer rows.Close()

	minEventDays := 365
	heartbeatDays := 30
	for rows.Next() {
		var cat string
		var days int
		if err := rows.Scan(&cat, &days); err != nil {
			continue
		}
		if cat == "heartbeat" {
			heartbeatDays = days
		} else if days < minEventDays {
			minEventDays = days
		}
	}
	rows.Close()

	cutoffs := []struct {
		prefix string
		days   int
	}{
		{"events", minEventDays},
		{"node_heartbeats", heartbeatDays},
	}

	for _, c := range cutoffs {
		cutoff := time.Now().UTC().AddDate(0, 0, -c.days)

		partRows, err := m.pool.Query(ctx, `
			SELECT child.relname
			FROM pg_inherits
			JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
			JOIN pg_class child  ON pg_inherits.inhrelid  = child.oid
			WHERE parent.relname = $1
			  AND child.relname NOT IN ($2, $3)
		`, c.prefix, c.prefix+"_default", c.prefix+"_default")
		if err != nil {
			slog.Warn("partition manager: list partitions failed", "table", c.prefix, "error", err)
			continue
		}

		var toDelete []string
		for partRows.Next() {
			var name string
			if err := partRows.Scan(&name); err != nil {
				continue
			}
			t, err := parsePartitionMonth(name)
			if err != nil {
				continue
			}
			// Partition covers [t, t+1month). It's expired when its end is before the cutoff.
			if t.AddDate(0, 1, 0).Before(cutoff) {
				toDelete = append(toDelete, name)
			}
		}
		partRows.Close()

		for _, name := range toDelete {
			slog.Info("partition manager: dropping expired partition", "partition", name)
			if _, err := m.pool.Exec(ctx,
				fmt.Sprintf(`ALTER TABLE %s DETACH PARTITION %s`, c.prefix, name),
			); err != nil {
				slog.Warn("partition manager: detach failed", "partition", name, "error", err)
				continue
			}
			if _, err := m.pool.Exec(ctx,
				fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name),
			); err != nil {
				slog.Warn("partition manager: drop failed", "partition", name, "error", err)
			}
		}
	}
	return nil
}

func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func parsePartitionMonth(name string) (time.Time, error) {
	if len(name) < 7 {
		return time.Time{}, fmt.Errorf("name too short")
	}
	suffix := name[len(name)-7:] // "2026_04"
	return time.Parse("2006_01", suffix)
}
