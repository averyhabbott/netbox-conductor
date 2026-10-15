ALTER TABLE backup_targets DROP COLUMN path_check;
ALTER TABLE backup_runs ADD COLUMN retry_after TIMESTAMPTZ;
CREATE INDEX ON backup_runs (status, retry_after) WHERE status IN ('pending', 'failed');
ALTER TABLE backup_runs DROP COLUMN repo_index;
