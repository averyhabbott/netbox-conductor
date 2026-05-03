CREATE TABLE cluster_patroni_designs (
    cluster_id  UUID        PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    config      JSONB       NOT NULL DEFAULT '{}',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE patroni_config_snapshots ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT false;

CREATE UNIQUE INDEX patroni_config_snapshots_one_active_per_cluster
    ON patroni_config_snapshots (cluster_id) WHERE is_active = true;

ALTER TABLE patroni_config_snapshots DROP CONSTRAINT patroni_config_snapshots_source_check;
ALTER TABLE patroni_config_snapshots ADD CONSTRAINT patroni_config_snapshots_source_check
    CHECK (source IN ('configure-failover', 'configure-backups', 'user-edit', 'user-revert', 'discovered'));
