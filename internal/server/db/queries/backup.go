package queries

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────────────────────────────────────────
// BackupTarget
// ─────────────────────────────────────────────────────────────

// BackupTarget represents one storage location in the backup_targets table.
// Up to 4 per cluster (repo_index 1-4), matching pgBackRest's repository limit.
type BackupTarget struct {
	ID          uuid.UUID
	ClusterID   uuid.UUID
	RepoIndex   int
	Label       string
	TargetType  string // "posix" | "s3" | "gcs" | "azure" | "sftp"

	// posix
	PosixPath *string

	// s3
	S3Bucket    *string
	S3Region    *string
	S3Endpoint  *string // nil = AWS default
	S3KeyIDEnc  *string
	S3SecretEnc *string

	// gcs
	GCSBucket *string
	GCSKeyEnc *string

	// azure
	AzureAccount   *string
	AzureContainer *string
	AzureKeyEnc    *string

	// sftp
	SFTPHost           *string
	SFTPPort           *int
	SFTPUser           *string
	SFTPPrivateKeyEnc  *string
	SFTPPath           *string

	// how far back in days the user wants to be able to restore
	RecoveryDays int

	// conductor-relayed local repo sync (posix only)
	SyncToNodes []uuid.UUID

	CreatedAt time.Time
	UpdatedAt time.Time
}

type BackupTargetQuerier struct {
	pool *pgxpool.Pool
}

func NewBackupTargetQuerier(pool *pgxpool.Pool) *BackupTargetQuerier {
	return &BackupTargetQuerier{pool: pool}
}

const backupTargetCols = `
	id, cluster_id, repo_index, label, target_type,
	posix_path,
	s3_bucket, s3_region, s3_endpoint, s3_key_id_enc, s3_secret_enc,
	gcs_bucket, gcs_key_enc,
	azure_account, azure_container, azure_key_enc,
	sftp_host, sftp_port, sftp_user, sftp_private_key_enc, sftp_path,
	recovery_days,
	sync_to_nodes,
	created_at, updated_at`

func scanBackupTarget(row interface{ Scan(...any) error }) (*BackupTarget, error) {
	var t BackupTarget
	err := row.Scan(
		&t.ID, &t.ClusterID, &t.RepoIndex, &t.Label, &t.TargetType,
		&t.PosixPath,
		&t.S3Bucket, &t.S3Region, &t.S3Endpoint, &t.S3KeyIDEnc, &t.S3SecretEnc,
		&t.GCSBucket, &t.GCSKeyEnc,
		&t.AzureAccount, &t.AzureContainer, &t.AzureKeyEnc,
		&t.SFTPHost, &t.SFTPPort, &t.SFTPUser, &t.SFTPPrivateKeyEnc, &t.SFTPPath,
		&t.RecoveryDays,
		&t.SyncToNodes,
		&t.CreatedAt, &t.UpdatedAt,
	)
	return &t, err
}

func (q *BackupTargetQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID) ([]BackupTarget, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT `+backupTargetCols+`
		FROM backup_targets WHERE cluster_id = $1 ORDER BY repo_index
	`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []BackupTarget
	for rows.Next() {
		t, err := scanBackupTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, *t)
	}
	return targets, rows.Err()
}

func (q *BackupTargetQuerier) Get(ctx context.Context, id uuid.UUID) (*BackupTarget, error) {
	return scanBackupTarget(q.pool.QueryRow(ctx, `
		SELECT `+backupTargetCols+` FROM backup_targets WHERE id = $1
	`, id))
}

// CreateBackupTargetParams holds the fields for inserting a new backup target.
type CreateBackupTargetParams struct {
	ClusterID  uuid.UUID
	RepoIndex  int
	Label      string
	TargetType string

	PosixPath *string

	S3Bucket    *string
	S3Region    *string
	S3Endpoint  *string
	S3KeyIDEnc  *string
	S3SecretEnc *string

	GCSBucket *string
	GCSKeyEnc *string

	AzureAccount   *string
	AzureContainer *string
	AzureKeyEnc    *string

	SFTPHost          *string
	SFTPPort          *int
	SFTPUser          *string
	SFTPPrivateKeyEnc *string
	SFTPPath          *string

	RecoveryDays int
	SyncToNodes  []uuid.UUID
}

func (q *BackupTargetQuerier) Create(ctx context.Context, p CreateBackupTargetParams) (*BackupTarget, error) {
	if p.SyncToNodes == nil {
		p.SyncToNodes = []uuid.UUID{}
	}
	return scanBackupTarget(q.pool.QueryRow(ctx, `
		INSERT INTO backup_targets (
			cluster_id, repo_index, label, target_type,
			posix_path,
			s3_bucket, s3_region, s3_endpoint, s3_key_id_enc, s3_secret_enc,
			gcs_bucket, gcs_key_enc,
			azure_account, azure_container, azure_key_enc,
			sftp_host, sftp_port, sftp_user, sftp_private_key_enc, sftp_path,
			recovery_days,
			sync_to_nodes
		) VALUES (
			$1,$2,$3,$4,
			$5,
			$6,$7,$8,$9,$10,
			$11,$12,
			$13,$14,$15,
			$16,$17,$18,$19,$20,
			$21,
			$22
		)
		RETURNING `+backupTargetCols,
		p.ClusterID, p.RepoIndex, p.Label, p.TargetType,
		p.PosixPath,
		p.S3Bucket, p.S3Region, p.S3Endpoint, p.S3KeyIDEnc, p.S3SecretEnc,
		p.GCSBucket, p.GCSKeyEnc,
		p.AzureAccount, p.AzureContainer, p.AzureKeyEnc,
		p.SFTPHost, p.SFTPPort, p.SFTPUser, p.SFTPPrivateKeyEnc, p.SFTPPath,
		p.RecoveryDays,
		p.SyncToNodes,
	))
}

// UpdateBackupTargetParams holds updatable fields for a backup target.
// Credentials are updated in place (caller must encrypt before passing).
type UpdateBackupTargetParams struct {
	ID    uuid.UUID
	Label string

	PosixPath *string

	S3Bucket    *string
	S3Region    *string
	S3Endpoint  *string
	S3KeyIDEnc  *string
	S3SecretEnc *string

	GCSBucket *string
	GCSKeyEnc *string

	AzureAccount   *string
	AzureContainer *string
	AzureKeyEnc    *string

	SFTPHost          *string
	SFTPPort          *int
	SFTPUser          *string
	SFTPPrivateKeyEnc *string
	SFTPPath          *string

	RecoveryDays int
	SyncToNodes  []uuid.UUID
}

func (q *BackupTargetQuerier) Update(ctx context.Context, p UpdateBackupTargetParams) (*BackupTarget, error) {
	if p.SyncToNodes == nil {
		p.SyncToNodes = []uuid.UUID{}
	}
	return scanBackupTarget(q.pool.QueryRow(ctx, `
		UPDATE backup_targets SET
			label = $2,
			posix_path = $3,
			s3_bucket = $4, s3_region = $5, s3_endpoint = $6, s3_key_id_enc = $7, s3_secret_enc = $8,
			gcs_bucket = $9, gcs_key_enc = $10,
			azure_account = $11, azure_container = $12, azure_key_enc = $13,
			sftp_host = $14, sftp_port = $15, sftp_user = $16, sftp_private_key_enc = $17, sftp_path = $18,
			recovery_days = $19,
			sync_to_nodes = $20,
			updated_at = now()
		WHERE id = $1
		RETURNING `+backupTargetCols,
		p.ID, p.Label,
		p.PosixPath,
		p.S3Bucket, p.S3Region, p.S3Endpoint, p.S3KeyIDEnc, p.S3SecretEnc,
		p.GCSBucket, p.GCSKeyEnc,
		p.AzureAccount, p.AzureContainer, p.AzureKeyEnc,
		p.SFTPHost, p.SFTPPort, p.SFTPUser, p.SFTPPrivateKeyEnc, p.SFTPPath,
		p.RecoveryDays,
		p.SyncToNodes,
	))
}

func (q *BackupTargetQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM backup_targets WHERE id = $1`, id)
	return err
}

// NextRepoIndex returns the lowest available repo_index (1-4) for a cluster.
// Returns 0 if all four slots are taken.
func (q *BackupTargetQuerier) NextRepoIndex(ctx context.Context, clusterID uuid.UUID) (int, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT repo_index FROM backup_targets WHERE cluster_id = $1 ORDER BY repo_index
	`, clusterID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	used := map[int]bool{}
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			return 0, err
		}
		used[idx] = true
	}
	for i := 1; i <= 4; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, nil
}

// ─────────────────────────────────────────────────────────────
// BackupSchedule
// ─────────────────────────────────────────────────────────────

type BackupSchedule struct {
	ClusterID            uuid.UUID
	Enabled              bool
	FullBackupCron       string
	DiffBackupCron       string
	IncrBackupIntervalHrs int
	StanzaName           *string
	StanzaInitialized    bool
	FirstBackupRun       bool
	RestoreInProgress    bool
	UpdatedAt            time.Time
}

type BackupScheduleQuerier struct {
	pool *pgxpool.Pool
}

func NewBackupScheduleQuerier(pool *pgxpool.Pool) *BackupScheduleQuerier {
	return &BackupScheduleQuerier{pool: pool}
}

func (q *BackupScheduleQuerier) Get(ctx context.Context, clusterID uuid.UUID) (*BackupSchedule, error) {
	var s BackupSchedule
	err := q.pool.QueryRow(ctx, `
		SELECT cluster_id, enabled,
			full_backup_cron, diff_backup_cron, incr_backup_interval_hrs,
			stanza_name, stanza_initialized, first_backup_run, restore_in_progress,
			updated_at
		FROM backup_schedules WHERE cluster_id = $1
	`, clusterID).Scan(
		&s.ClusterID, &s.Enabled,
		&s.FullBackupCron, &s.DiffBackupCron, &s.IncrBackupIntervalHrs,
		&s.StanzaName, &s.StanzaInitialized, &s.FirstBackupRun, &s.RestoreInProgress,
		&s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListEnabled returns all clusters with backup enabled and stanza initialized.
func (q *BackupScheduleQuerier) ListEnabled(ctx context.Context) ([]BackupSchedule, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT cluster_id, enabled,
			full_backup_cron, diff_backup_cron, incr_backup_interval_hrs,
			stanza_name, stanza_initialized, first_backup_run, restore_in_progress,
			updated_at
		FROM backup_schedules
		WHERE enabled = TRUE AND stanza_initialized = TRUE AND restore_in_progress = FALSE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []BackupSchedule
	for rows.Next() {
		var s BackupSchedule
		if err := rows.Scan(
			&s.ClusterID, &s.Enabled,
			&s.FullBackupCron, &s.DiffBackupCron, &s.IncrBackupIntervalHrs,
			&s.StanzaName, &s.StanzaInitialized, &s.FirstBackupRun, &s.RestoreInProgress,
			&s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

type UpsertBackupScheduleParams struct {
	ClusterID             uuid.UUID
	Enabled               bool
	FullBackupCron        string
	DiffBackupCron        string
	IncrBackupIntervalHrs int
}

func (q *BackupScheduleQuerier) Upsert(ctx context.Context, p UpsertBackupScheduleParams) (*BackupSchedule, error) {
	var s BackupSchedule
	err := q.pool.QueryRow(ctx, `
		INSERT INTO backup_schedules (cluster_id, enabled, full_backup_cron, diff_backup_cron, incr_backup_interval_hrs)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cluster_id) DO UPDATE
		  SET enabled = EXCLUDED.enabled,
		      full_backup_cron = EXCLUDED.full_backup_cron,
		      diff_backup_cron = EXCLUDED.diff_backup_cron,
		      incr_backup_interval_hrs = EXCLUDED.incr_backup_interval_hrs,
		      updated_at = now()
		RETURNING cluster_id, enabled,
			full_backup_cron, diff_backup_cron, incr_backup_interval_hrs,
			stanza_name, stanza_initialized, first_backup_run, restore_in_progress,
			updated_at
	`, p.ClusterID, p.Enabled, p.FullBackupCron, p.DiffBackupCron, p.IncrBackupIntervalHrs).Scan(
		&s.ClusterID, &s.Enabled,
		&s.FullBackupCron, &s.DiffBackupCron, &s.IncrBackupIntervalHrs,
		&s.StanzaName, &s.StanzaInitialized, &s.FirstBackupRun, &s.RestoreInProgress,
		&s.UpdatedAt,
	)
	return &s, err
}

func (q *BackupScheduleQuerier) SetStanzaInitialized(ctx context.Context, clusterID uuid.UUID, stanzaName string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_schedules
		SET stanza_name = $2, stanza_initialized = TRUE, updated_at = now()
		WHERE cluster_id = $1
	`, clusterID, stanzaName)
	return err
}

func (q *BackupScheduleQuerier) SetFirstBackupRun(ctx context.Context, clusterID uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_schedules SET first_backup_run = TRUE, updated_at = now() WHERE cluster_id = $1
	`, clusterID)
	return err
}

func (q *BackupScheduleQuerier) SetRestoreInProgress(ctx context.Context, clusterID uuid.UUID, inProgress bool) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_schedules SET restore_in_progress = $2, updated_at = now() WHERE cluster_id = $1
	`, clusterID, inProgress)
	return err
}

// ─────────────────────────────────────────────────────────────
// BackupRun
// ─────────────────────────────────────────────────────────────

type BackupRun struct {
	ID           uuid.UUID
	ClusterID    uuid.UUID
	BackupType   string // "full" | "diff" | "incr"
	TaskID       *uuid.UUID
	Attempt      int
	ScheduledAt  time.Time
	DispatchedAt *time.Time
	Status       string
	RetryAfter   *time.Time
	CompletedAt  *time.Time
	ErrorMessage *string
}

type BackupRunQuerier struct {
	pool *pgxpool.Pool
}

func NewBackupRunQuerier(pool *pgxpool.Pool) *BackupRunQuerier {
	return &BackupRunQuerier{pool: pool}
}

func (q *BackupRunQuerier) Create(ctx context.Context, clusterID uuid.UUID, backupType string, scheduledAt time.Time, attempt int) (*BackupRun, error) {
	var r BackupRun
	err := q.pool.QueryRow(ctx, `
		INSERT INTO backup_runs (cluster_id, backup_type, scheduled_at, attempt)
		VALUES ($1, $2, $3, $4)
		RETURNING id, cluster_id, backup_type, task_id, attempt, scheduled_at,
			dispatched_at, status, retry_after, completed_at, error_message
	`, clusterID, backupType, scheduledAt, attempt).Scan(
		&r.ID, &r.ClusterID, &r.BackupType, &r.TaskID, &r.Attempt, &r.ScheduledAt,
		&r.DispatchedAt, &r.Status, &r.RetryAfter, &r.CompletedAt, &r.ErrorMessage,
	)
	return &r, err
}

func (q *BackupRunQuerier) SetDispatched(ctx context.Context, id, taskID uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_runs
		SET task_id = $2, status = 'running', dispatched_at = now()
		WHERE id = $1
	`, id, taskID)
	return err
}

func (q *BackupRunQuerier) SetSuccess(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_runs SET status = 'success', completed_at = now() WHERE id = $1
	`, id)
	return err
}

func (q *BackupRunQuerier) SetFailed(ctx context.Context, id uuid.UUID, errMsg string, retryAfter *time.Time) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_runs
		SET status = 'failed', completed_at = now(), error_message = $2, retry_after = $3
		WHERE id = $1
	`, id, errMsg, retryAfter)
	return err
}

func (q *BackupRunQuerier) SetAbandoned(ctx context.Context, id uuid.UUID, errMsg string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_runs
		SET status = 'abandoned', completed_at = now(), error_message = $2
		WHERE id = $1
	`, id, errMsg)
	return err
}

// CompletedBackupRun pairs a backup_run ID with the terminal status and response
// payload of the task it was waiting on. Returned by ListRunningWithCompletedTask.
type CompletedBackupRun struct {
	RunID           uuid.UUID
	ClusterID       uuid.UUID
	TaskStatus      string
	ResponsePayload json.RawMessage
}

// ListRunningWithCompletedTask returns backup_runs that are still marked 'running'
// but whose associated task_result has reached a terminal state. Used by the
// scheduler to flip run status once the agent reports back.
func (q *BackupRunQuerier) ListRunningWithCompletedTask(ctx context.Context) ([]CompletedBackupRun, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT br.id, br.cluster_id, tr.status, tr.response_payload
		FROM backup_runs br
		JOIN task_results tr ON tr.task_id = br.task_id
		WHERE br.status = 'running'
		  AND tr.status IN ('success', 'failure', 'timeout')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []CompletedBackupRun
	for rows.Next() {
		var r CompletedBackupRun
		if err := rows.Scan(&r.RunID, &r.ClusterID, &r.TaskStatus, &r.ResponsePayload); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SetRetrying updates an existing backup_run in-place for a retry attempt.
// The same row is reused — attempt increments, task_id and dispatched_at update,
// retry_after clears, and status returns to 'running'. No new row is inserted.
func (q *BackupRunQuerier) SetRetrying(ctx context.Context, id, taskID uuid.UUID, newAttempt int) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE backup_runs
		SET task_id=$2, attempt=$3, status='running', dispatched_at=now(), retry_after=NULL
		WHERE id=$1
	`, id, taskID, newAttempt)
	return err
}

// PendingRetries returns failed runs whose retry_after has passed.
func (q *BackupRunQuerier) PendingRetries(ctx context.Context) ([]BackupRun, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, backup_type, task_id, attempt, scheduled_at,
			dispatched_at, status, retry_after, completed_at, error_message
		FROM backup_runs
		WHERE status = 'failed' AND retry_after <= now()
		ORDER BY retry_after
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBackupRuns(rows)
}

// ListByCluster returns the most recent backup runs for a cluster.
func (q *BackupRunQuerier) ListByCluster(ctx context.Context, clusterID uuid.UUID, limit int) ([]BackupRun, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, cluster_id, backup_type, task_id, attempt, scheduled_at,
			dispatched_at, status, retry_after, completed_at, error_message
		FROM backup_runs
		WHERE cluster_id = $1
		ORDER BY scheduled_at DESC
		LIMIT $2
	`, clusterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBackupRuns(rows)
}

func scanBackupRuns(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]BackupRun, error) {
	var runs []BackupRun
	for rows.Next() {
		var r BackupRun
		if err := rows.Scan(
			&r.ID, &r.ClusterID, &r.BackupType, &r.TaskID, &r.Attempt, &r.ScheduledAt,
			&r.DispatchedAt, &r.Status, &r.RetryAfter, &r.CompletedAt, &r.ErrorMessage,
		); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ─────────────────────────────────────────────────────────────
// BackupCatalogCache
// ─────────────────────────────────────────────────────────────

type BackupCatalogCache struct {
	ID                  uuid.UUID
	ClusterID           uuid.UUID
	FetchedAt           time.Time
	CatalogJSON         []byte
	OldestRestorePoint  *time.Time
	NewestRestorePoint  *time.Time
}

type BackupCatalogQuerier struct {
	pool *pgxpool.Pool
}

func NewBackupCatalogQuerier(pool *pgxpool.Pool) *BackupCatalogQuerier {
	return &BackupCatalogQuerier{pool: pool}
}

// GetLatest returns the most recently fetched catalog for a cluster.
func (q *BackupCatalogQuerier) GetLatest(ctx context.Context, clusterID uuid.UUID) (*BackupCatalogCache, error) {
	var c BackupCatalogCache
	err := q.pool.QueryRow(ctx, `
		SELECT id, cluster_id, fetched_at, catalog_json, oldest_restore_point, newest_restore_point
		FROM backup_catalog_cache
		WHERE cluster_id = $1
		ORDER BY fetched_at DESC
		LIMIT 1
	`, clusterID).Scan(
		&c.ID, &c.ClusterID, &c.FetchedAt, &c.CatalogJSON,
		&c.OldestRestorePoint, &c.NewestRestorePoint,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (q *BackupCatalogQuerier) Upsert(ctx context.Context, clusterID uuid.UUID, catalogJSON []byte, oldest, newest *time.Time) (*BackupCatalogCache, error) {
	var c BackupCatalogCache
	err := q.pool.QueryRow(ctx, `
		INSERT INTO backup_catalog_cache (cluster_id, catalog_json, oldest_restore_point, newest_restore_point)
		VALUES ($1, $2, $3, $4)
		RETURNING id, cluster_id, fetched_at, catalog_json, oldest_restore_point, newest_restore_point
	`, clusterID, catalogJSON, oldest, newest).Scan(
		&c.ID, &c.ClusterID, &c.FetchedAt, &c.CatalogJSON,
		&c.OldestRestorePoint, &c.NewestRestorePoint,
	)
	return &c, err
}

// Prune removes catalog entries older than the N most recent for a cluster.
func (q *BackupCatalogQuerier) Prune(ctx context.Context, clusterID uuid.UUID, keep int) error {
	_, err := q.pool.Exec(ctx, `
		DELETE FROM backup_catalog_cache
		WHERE cluster_id = $1
		  AND id NOT IN (
		      SELECT id FROM backup_catalog_cache
		      WHERE cluster_id = $1
		      ORDER BY fetched_at DESC
		      LIMIT $2
		  )
	`, clusterID, keep)
	return err
}
