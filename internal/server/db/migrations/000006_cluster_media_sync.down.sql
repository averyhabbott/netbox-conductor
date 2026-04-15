-- 000006_cluster_media_sync.down.sql

ALTER TABLE clusters
  DROP COLUMN media_sync_enabled,
  DROP COLUMN extra_folders_sync_enabled,
  DROP COLUMN extra_sync_folders;
