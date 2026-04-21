ALTER TABLE backup_targets
  DROP COLUMN recovery_days,
  ADD COLUMN full_retention INT NOT NULL DEFAULT 2,
  ADD COLUMN diff_retention INT NOT NULL DEFAULT 7,
  ADD COLUMN wal_retention_days INT NOT NULL DEFAULT 14;
