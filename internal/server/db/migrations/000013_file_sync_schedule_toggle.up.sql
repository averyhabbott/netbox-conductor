-- The file sync schedule gets its own on/off switch (off by default), so
-- turning it off keeps the cron expression. Every 30 minutes by default.
ALTER TABLE clusters ADD COLUMN file_sync_schedule_enabled BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE clusters SET file_sync_schedule_enabled = TRUE WHERE file_sync_cron <> '';
UPDATE clusters SET file_sync_cron = '*/30 * * * *' WHERE file_sync_cron = '';
ALTER TABLE clusters ALTER COLUMN file_sync_cron SET DEFAULT '*/30 * * * *';
