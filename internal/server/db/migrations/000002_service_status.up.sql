ALTER TABLE nodes
    ADD COLUMN redis_running    BOOLEAN,
    ADD COLUMN redis_role       TEXT,
    ADD COLUMN sentinel_running BOOLEAN,
    ADD COLUMN patroni_running  BOOLEAN,
    ADD COLUMN postgres_running BOOLEAN;
