package queries

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FileSyncRecord is what two-way file sync remembers about one folder of a
// cluster (folder "" = the NetBox media folder).
type FileSyncRecord struct {
	Files    map[uuid.UUID]map[string]string // node → path → SHA-256 at its last successful sync
	SyncedAt map[uuid.UUID]time.Time         // node → when that was; no entry = never synced
	Deleted  map[string]bool                 // paths some node deleted that others may still have
}

type FileSyncQuerier struct {
	pool *pgxpool.Pool
}

func NewFileSyncQuerier(pool *pgxpool.Pool) *FileSyncQuerier {
	return &FileSyncQuerier{pool: pool}
}

// Load returns the record of one folder.
func (q *FileSyncQuerier) Load(ctx context.Context, clusterID uuid.UUID, folder string) (FileSyncRecord, error) {
	rec := FileSyncRecord{
		Files:    map[uuid.UUID]map[string]string{},
		SyncedAt: map[uuid.UUID]time.Time{},
		Deleted:  map[string]bool{},
	}
	rows, err := q.pool.Query(ctx, `
		SELECT node_id, synced_at FROM file_sync_nodes WHERE cluster_id = $1 AND folder = $2`, clusterID, folder)
	if err != nil {
		return rec, err
	}
	var node uuid.UUID
	var at time.Time
	if _, err := pgx.ForEachRow(rows, []any{&node, &at}, func() error {
		rec.SyncedAt[node] = at
		rec.Files[node] = map[string]string{}
		return nil
	}); err != nil {
		return rec, err
	}

	rows, err = q.pool.Query(ctx, `
		SELECT node_id, path, sha256 FROM file_sync_files WHERE cluster_id = $1 AND folder = $2`, clusterID, folder)
	if err != nil {
		return rec, err
	}
	var path, sum string
	if _, err := pgx.ForEachRow(rows, []any{&node, &path, &sum}, func() error {
		rec.Files[node][path] = sum
		return nil
	}); err != nil {
		return rec, err
	}

	rows, err = q.pool.Query(ctx, `
		SELECT path FROM file_sync_deletes WHERE cluster_id = $1 AND folder = $2`, clusterID, folder)
	if err != nil {
		return rec, err
	}
	_, err = pgx.ForEachRow(rows, []any{&path}, func() error {
		rec.Deleted[path] = true
		return nil
	})
	return rec, err
}

// Save records the outcome of one sync in a single transaction: each node in
// synced now holds files (path → SHA-256); deleted paths are remembered until
// every app-tier and hyperconverged node of the cluster has synced since.
func (q *FileSyncQuerier) Save(ctx context.Context, clusterID uuid.UUID, folder string, synced []uuid.UUID, files map[string]string, deleted []string) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Lock the cluster row so a settings change removing this folder either
	// waits for this save (its KeepFolders then clears it) or is seen here.
	// Same rule as filesync.SyncFolders.
	var wanted bool
	if err := tx.QueryRow(ctx, `
		SELECT $2::text = '' OR (extra_folders_sync_enabled AND $2::text = ANY(extra_sync_folders))
		FROM clusters WHERE id = $1 FOR UPDATE`, clusterID, folder).Scan(&wanted); err != nil {
		return err
	}
	if !wanted {
		return nil
	}

	paths := slices.Collect(maps.Keys(files))
	for _, node := range synced {
		if _, err := tx.Exec(ctx, `
			INSERT INTO file_sync_nodes (cluster_id, folder, node_id, synced_at) VALUES ($1, $2, $3, now())
			ON CONFLICT (cluster_id, folder, node_id) DO UPDATE SET synced_at = now()`,
			clusterID, folder, node); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM file_sync_files WHERE cluster_id = $1 AND folder = $2 AND node_id = $3`,
			clusterID, folder, node); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"file_sync_files"},
			[]string{"cluster_id", "folder", "node_id", "path", "sha256"},
			pgx.CopyFromSlice(len(paths), func(i int) ([]any, error) {
				return []any{clusterID, folder, node, paths[i], files[paths[i]]}, nil
			})); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO file_sync_deletes (cluster_id, folder, path)
		SELECT $1, $2, unnest($3::text[])
		ON CONFLICT DO NOTHING`, clusterID, folder, deleted); err != nil {
		return err
	}
	// A file that exists again (an edit beat a delete) is no longer deleted.
	if _, err := tx.Exec(ctx, `
		DELETE FROM file_sync_deletes WHERE cluster_id = $1 AND folder = $2 AND path = ANY($3)`,
		clusterID, folder, paths); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM file_sync_deletes d
		WHERE d.cluster_id = $1 AND d.folder = $2 AND NOT EXISTS (
			SELECT 1 FROM nodes n
			LEFT JOIN file_sync_nodes s ON s.cluster_id = n.cluster_id AND s.folder = $2 AND s.node_id = n.id
			WHERE n.cluster_id = $1 AND n.role IN ('app', 'hyperconverged')
			  AND (s.synced_at IS NULL OR s.synced_at < d.deleted_at))`,
		clusterID, folder); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// KeepFolders forgets the record of every folder of the cluster not in keep,
// so a folder removed and added back later starts over (copy only) instead of
// treating files changed in between as deletes.
func (q *FileSyncQuerier) KeepFolders(ctx context.Context, clusterID uuid.UUID, keep []string) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, table := range []string{"file_sync_nodes", "file_sync_deletes"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE cluster_id = $1 AND NOT folder = ANY($2)`,
			clusterID, keep); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
