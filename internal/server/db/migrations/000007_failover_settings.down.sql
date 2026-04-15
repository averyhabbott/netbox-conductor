-- 000007_failover_settings.down.sql

ALTER TABLE clusters
  DROP COLUMN IF EXISTS app_tier_always_available,
  DROP COLUMN IF EXISTS failover_on_maintenance,
  DROP COLUMN IF EXISTS failover_delay_secs;

ALTER TABLE clusters
  ALTER COLUMN auto_failover SET DEFAULT FALSE,
  ALTER COLUMN auto_failback SET DEFAULT FALSE;
