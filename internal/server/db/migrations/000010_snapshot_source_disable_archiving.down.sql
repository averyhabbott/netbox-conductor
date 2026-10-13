DELETE FROM patroni_config_snapshots WHERE source = 'disable-archiving';
ALTER TABLE patroni_config_snapshots DROP CONSTRAINT patroni_config_snapshots_source_check;
ALTER TABLE patroni_config_snapshots ADD CONSTRAINT patroni_config_snapshots_source_check
    CHECK (source IN ('configure-failover', 'configure-backups', 'user-edit', 'user-revert', 'discovered'));
