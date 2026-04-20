-- 000004_backup.up.sql
--
-- Adds tables for conductor-managed pgBackRest backup configuration,
-- scheduling, run tracking, and catalog caching.

-- ──────────────────────────────────────────────
-- backup_targets
-- Up to 4 storage locations per cluster, matching pgBackRest's repo1-repo4.
-- Supported types: posix (local disk / NFS / SMB mount), s3, gcs, azure, sftp.
-- ──────────────────────────────────────────────
CREATE TABLE backup_targets (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id      UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    repo_index      INT         NOT NULL CHECK (repo_index BETWEEN 1 AND 4),
    label           TEXT        NOT NULL,
    target_type     TEXT        NOT NULL CHECK (target_type IN ('posix', 's3', 'gcs', 'azure', 'sftp')),

    -- posix: local filesystem, NFS mount, or SMB mount (mount managed externally)
    posix_path      TEXT,

    -- s3 / s3-compatible (MinIO, Wasabi, Backblaze B2, etc.)
    s3_bucket       TEXT,
    s3_region       TEXT,
    s3_endpoint     TEXT,           -- empty = AWS default endpoint
    s3_key_id_enc   TEXT,           -- AES-256-GCM encrypted access key ID
    s3_secret_enc   TEXT,           -- AES-256-GCM encrypted secret key

    -- gcs
    gcs_bucket      TEXT,
    gcs_key_enc     TEXT,           -- AES-256-GCM encrypted service account JSON

    -- azure
    azure_account   TEXT,
    azure_container TEXT,
    azure_key_enc   TEXT,           -- AES-256-GCM encrypted access key

    -- sftp
    sftp_host       TEXT,
    sftp_port       INT             DEFAULT 22,
    sftp_user       TEXT,
    sftp_private_key_enc TEXT,      -- AES-256-GCM encrypted private key
    sftp_path       TEXT,

    -- retention (maps to pgBackRest repo{N}-retention-* per repo)
    full_retention      INT         NOT NULL DEFAULT 2,
    diff_retention      INT         NOT NULL DEFAULT 7,
    wal_retention_days  INT         NOT NULL DEFAULT 14,

    -- conductor-relayed local repo sync (posix targets only; ignored for others)
    sync_to_nodes   UUID[]          NOT NULL DEFAULT '{}',

    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),

    UNIQUE (cluster_id, repo_index),
    UNIQUE (cluster_id, label)
);

CREATE INDEX ON backup_targets (cluster_id);

-- ──────────────────────────────────────────────
-- backup_schedules
-- One row per cluster. Stores the three-tier backup schedule and
-- pgBackRest bootstrap state.
-- ──────────────────────────────────────────────
CREATE TABLE backup_schedules (
    cluster_id               UUID        PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    enabled                  BOOLEAN     NOT NULL DEFAULT TRUE,

    -- cron expressions (UTC); generated from plain-English UI dropdowns
    full_backup_cron         TEXT        NOT NULL DEFAULT '0 1 * * 0',    -- weekly Sun 1am
    diff_backup_cron         TEXT        NOT NULL DEFAULT '0 1 * * 1-6',  -- daily Mon-Sat 1am
    incr_backup_interval_hrs INT         NOT NULL DEFAULT 1 CHECK (incr_backup_interval_hrs BETWEEN 1 AND 24),

    -- pgBackRest bootstrap state
    stanza_name              TEXT,                        -- set at first-enable, locked after first backup
    stanza_initialized       BOOLEAN     NOT NULL DEFAULT FALSE,
    first_backup_run         BOOLEAN     NOT NULL DEFAULT FALSE, -- once true, stanza_name is immutable

    -- restore lock: blocks scheduled backups while a restore is in progress
    restore_in_progress      BOOLEAN     NOT NULL DEFAULT FALSE,

    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────
-- backup_runs
-- Tracks individual backup job attempts dispatched by the conductor scheduler.
-- Supports 3-attempt retry with 5-minute pause on failure.
-- ──────────────────────────────────────────────
CREATE TABLE backup_runs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id      UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    backup_type     TEXT        NOT NULL CHECK (backup_type IN ('full', 'diff', 'incr')),
    task_id         UUID        REFERENCES task_results(task_id) ON DELETE SET NULL,
    attempt         INT         NOT NULL DEFAULT 1 CHECK (attempt BETWEEN 1 AND 3),
    scheduled_at    TIMESTAMPTZ NOT NULL,
    dispatched_at   TIMESTAMPTZ,
    status          TEXT        NOT NULL DEFAULT 'pending'
                                    CHECK (status IN ('pending', 'running', 'success', 'failed', 'abandoned')),
    retry_after     TIMESTAMPTZ,    -- set on failure; scheduler re-dispatches after this time
    completed_at    TIMESTAMPTZ,
    error_message   TEXT            -- human-readable error for UI display
);

CREATE INDEX ON backup_runs (cluster_id, scheduled_at DESC);
CREATE INDEX ON backup_runs (status, retry_after) WHERE status IN ('pending', 'failed');

-- ──────────────────────────────────────────────
-- backup_catalog_cache
-- Cached output of `pgbackrest info --output=json` from the primary node.
-- Refreshed on-demand; used to populate the restore point slider bounds.
-- ──────────────────────────────────────────────
CREATE TABLE backup_catalog_cache (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id           UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    fetched_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    catalog_json         JSONB       NOT NULL,
    oldest_restore_point TIMESTAMPTZ,
    newest_restore_point TIMESTAMPTZ
);

CREATE INDEX ON backup_catalog_cache (cluster_id, fetched_at DESC);
