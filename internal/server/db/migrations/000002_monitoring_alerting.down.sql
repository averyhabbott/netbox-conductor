-- 000002_monitoring_alerting.down.sql

DROP TABLE IF EXISTS event_retention        CASCADE;
DROP TABLE IF EXISTS syslog_destinations    CASCADE;
DROP TABLE IF EXISTS active_alert_states    CASCADE;
DROP TABLE IF EXISTS alert_rule_transports  CASCADE;
DROP TABLE IF EXISTS alert_rules            CASCADE;
DROP TABLE IF EXISTS alert_schedules        CASCADE;
DROP TABLE IF EXISTS alert_transports       CASCADE;
DROP TABLE IF EXISTS node_heartbeats        CASCADE;
DROP TABLE IF EXISTS events                 CASCADE;

-- Restore legacy tables (minimal schema — no data recovery).
CREATE TABLE retention_policies (
    cluster_id     UUID PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    retention_days INT  NOT NULL DEFAULT 7 CHECK (retention_days > 0),
    expire_cmd     TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE node_log_entries (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id  UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    node_id     UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    hostname    TEXT        NOT NULL DEFAULT '',
    level       TEXT        NOT NULL DEFAULT 'info',
    source      TEXT        NOT NULL DEFAULT 'conductor',
    message     TEXT        NOT NULL,
    log_file    TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE failover_events (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id       UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    event_type       TEXT        NOT NULL,
    trigger          TEXT        NOT NULL,
    failed_node_id   UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    failed_node_name TEXT,
    target_node_id   UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    target_node_name TEXT,
    success          BOOLEAN     NOT NULL,
    reason           TEXT,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alert_configs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    type        TEXT        NOT NULL,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    conditions  JSONB       NOT NULL DEFAULT '[]',
    webhook_url TEXT,
    email_to    TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE active_alerts (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id      UUID        REFERENCES clusters(id) ON DELETE CASCADE,
    node_id         UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    severity        TEXT        NOT NULL,
    condition       TEXT        NOT NULL,
    message         TEXT        NOT NULL,
    fired_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ
);
