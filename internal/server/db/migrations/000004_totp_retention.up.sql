-- 000004_totp_retention.up.sql

-- Explicit flag so TOTP can be enrolled (secret stored) before it is confirmed/activated.
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT FALSE;

-- Retention policies: one row per cluster, configured by operators.
CREATE TABLE IF NOT EXISTS retention_policies (
    cluster_id       UUID PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    retention_days   INT  NOT NULL DEFAULT 7
                         CHECK (retention_days > 0),
    expire_cmd       TEXT,             -- optional override; NULL = use pgbackrest default
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
