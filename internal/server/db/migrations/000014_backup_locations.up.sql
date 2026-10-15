-- Backups run once per storage location: each run records its location's
-- pgBackRest repo number. A failed backup isn't retried (the next scheduled
-- one runs as usual), so a run's attempt is only its round's try to start.
ALTER TABLE backup_runs ADD COLUMN repo_index INT;
ALTER TABLE backup_runs DROP COLUMN retry_after;

-- Last path test result of a local-disk location on each node:
-- {"<node id>": "<error>"}, "" when the test passed.
ALTER TABLE backup_targets ADD COLUMN path_check JSONB NOT NULL DEFAULT '{}';
