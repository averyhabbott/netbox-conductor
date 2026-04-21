-- Replace three separate retention knobs with a single recovery_days value.
-- The conductor derives full_retention, diff_retention, and wal_retention_days
-- from this one field when rendering pgbackrest.conf.
ALTER TABLE backup_targets
  ADD COLUMN recovery_days INT NOT NULL DEFAULT 14;

-- Preserve existing intent: wal_retention_days was the binding constraint,
-- so back-calculate recovery_days = wal_retention_days - 7 (our buffer).
UPDATE backup_targets SET recovery_days = GREATEST(7, wal_retention_days - 7);

ALTER TABLE backup_targets
  DROP COLUMN full_retention,
  DROP COLUMN diff_retention,
  DROP COLUMN wal_retention_days;
