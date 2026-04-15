-- 000004_totp_retention.down.sql
DROP TABLE IF EXISTS retention_policies;
ALTER TABLE users DROP COLUMN IF EXISTS totp_enabled;
