ALTER TABLE clusters
  DROP COLUMN IF EXISTS patroni_configured,
  DROP COLUMN IF EXISTS redis_sentinel_master;
