UPDATE clusters SET file_sync_cron = '' WHERE NOT file_sync_schedule_enabled;
ALTER TABLE clusters ALTER COLUMN file_sync_cron SET DEFAULT '';
ALTER TABLE clusters DROP COLUMN file_sync_schedule_enabled;
