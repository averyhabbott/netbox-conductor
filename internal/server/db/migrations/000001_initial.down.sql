-- 000001_initial.down.sql

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS active_alerts;
DROP TABLE IF EXISTS alert_configs;
DROP TABLE IF EXISTS node_log_entries;
DROP TABLE IF EXISTS failover_events;
DROP TABLE IF EXISTS task_results;
DROP TABLE IF EXISTS retention_policies;
DROP TABLE IF EXISTS netbox_config_overrides;
DROP TABLE IF EXISTS netbox_configs;
DROP TABLE IF EXISTS credentials;
DROP TABLE IF EXISTS staging_agents;
DROP TABLE IF EXISTS staging_tokens;
DROP TABLE IF EXISTS registration_tokens;
DROP TABLE IF EXISTS agent_tokens;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS clusters;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
