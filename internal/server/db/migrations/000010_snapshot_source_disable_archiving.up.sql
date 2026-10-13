-- Deleting the last backup target turns WAL archiving off and records a
-- post-change snapshot with source 'disable-archiving'; allow it.
ALTER TABLE patroni_config_snapshots DROP CONSTRAINT patroni_config_snapshots_source_check;
ALTER TABLE patroni_config_snapshots ADD CONSTRAINT patroni_config_snapshots_source_check
    CHECK (source IN ('configure-failover', 'configure-backups', 'user-edit', 'user-revert', 'discovered', 'disable-archiving'));
