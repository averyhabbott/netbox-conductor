ALTER TABLE clusters
  ADD COLUMN patroni_configured    BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN redis_sentinel_master TEXT    NOT NULL DEFAULT 'netbox';
