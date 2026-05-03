ALTER TABLE patroni_config_snapshots DROP CONSTRAINT patroni_config_snapshots_source_check;
ALTER TABLE patroni_config_snapshots ADD CONSTRAINT patroni_config_snapshots_source_check
    CHECK (source IN ('configure-failover', 'configure-backups', 'user-edit'));

DROP INDEX IF EXISTS patroni_config_snapshots_one_active_per_cluster;
ALTER TABLE patroni_config_snapshots DROP COLUMN IF EXISTS is_active;

DROP TABLE IF EXISTS cluster_patroni_designs;
