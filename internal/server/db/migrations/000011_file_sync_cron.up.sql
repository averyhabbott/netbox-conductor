-- Schedule for syncing media and extra folders (5-field cron, same format as
-- the backup schedule). Empty means manual "Sync now" only.
ALTER TABLE clusters ADD COLUMN file_sync_cron TEXT NOT NULL DEFAULT '';
