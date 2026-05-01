CREATE TABLE patroni_config_snapshots (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id  UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    source      TEXT        NOT NULL CHECK (source IN ('configure-failover', 'configure-backups', 'user-edit')),
    config      JSONB       NOT NULL
);

CREATE INDEX ON patroni_config_snapshots (cluster_id, captured_at DESC);
