-- 000006_cluster_media_sync.up.sql
-- Cluster-level media sync configuration.

ALTER TABLE clusters
  ADD COLUMN media_sync_enabled        BOOLEAN  NOT NULL DEFAULT FALSE,
  ADD COLUMN extra_folders_sync_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN extra_sync_folders         TEXT[]  NOT NULL DEFAULT '{}';
