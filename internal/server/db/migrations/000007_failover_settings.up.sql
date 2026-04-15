-- 000007_failover_settings.up.sql
-- Extended failover configuration for active_standby clusters.

ALTER TABLE clusters
  ADD COLUMN app_tier_always_available BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN failover_on_maintenance   BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN failover_delay_secs       INTEGER NOT NULL DEFAULT 30;

-- Flip column defaults so newly-created clusters are opted-in to auto failover/failback.
ALTER TABLE clusters
  ALTER COLUMN auto_failover SET DEFAULT TRUE,
  ALTER COLUMN auto_failback SET DEFAULT TRUE;
