-- 000005_failover_priority.down.sql
ALTER TABLE nodes ALTER COLUMN failover_priority SET DEFAULT 100;
UPDATE nodes SET failover_priority = 100 WHERE failover_priority = 50;
