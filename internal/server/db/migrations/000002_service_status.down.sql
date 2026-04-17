ALTER TABLE nodes
    DROP COLUMN IF EXISTS redis_running,
    DROP COLUMN IF EXISTS redis_role,
    DROP COLUMN IF EXISTS sentinel_running,
    DROP COLUMN IF EXISTS patroni_running,
    DROP COLUMN IF EXISTS postgres_running;
