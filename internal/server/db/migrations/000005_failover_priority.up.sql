-- 000005_failover_priority.up.sql
-- Change failover_priority scale: higher number = more preferred (was lower = preferred).
-- New range: 1–100, default 50.

ALTER TABLE nodes ALTER COLUMN failover_priority SET DEFAULT 50;

-- Remap existing values: old default 100 → new default 50; otherwise clamp to 1–100.
UPDATE nodes SET failover_priority = 50 WHERE failover_priority = 100;
UPDATE nodes SET failover_priority = GREATEST(1, LEAST(100, failover_priority)) WHERE failover_priority != 50;
