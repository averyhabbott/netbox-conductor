-- 000002_monitoring_alerting.up.sql
--
-- Replaces the fragmented node_log_entries, failover_events, alert_configs,
-- active_alerts, and retention_policies tables with a cohesive event and
-- alerting system.

-- ──────────────────────────────────────────────────────────────────────────────
-- Drop legacy tables (clean break)
-- ──────────────────────────────────────────────────────────────────────────────
DROP TABLE IF EXISTS active_alerts      CASCADE;
DROP TABLE IF EXISTS alert_configs      CASCADE;
DROP TABLE IF EXISTS failover_events    CASCADE;
DROP TABLE IF EXISTS node_log_entries   CASCADE;
DROP TABLE IF EXISTS retention_policies CASCADE;

-- ──────────────────────────────────────────────────────────────────────────────
-- events  (partitioned by month)
-- Unified event log covering cluster, service, ha, config, and agent events.
-- The partition key (occurred_at) is part of the primary key as required by PG.
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE events (
    id          UUID        NOT NULL DEFAULT gen_random_uuid(),
    cluster_id  UUID        REFERENCES clusters(id) ON DELETE CASCADE,
    node_id     UUID        REFERENCES nodes(id)    ON DELETE SET NULL,
    category    TEXT        NOT NULL CHECK (category IN ('cluster','service','ha','config','agent')),
    severity    TEXT        NOT NULL DEFAULT 'info'
                                CHECK (severity IN ('debug','info','warn','error','critical')),
    code        TEXT        NOT NULL,
    message     TEXT        NOT NULL,
    actor       TEXT        NOT NULL DEFAULT 'system',
    metadata    JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX events_cluster_idx    ON events (cluster_id, occurred_at DESC);
CREATE INDEX events_node_idx       ON events (node_id,    occurred_at DESC);
CREATE INDEX events_category_idx   ON events (category,   occurred_at DESC);
CREATE INDEX events_severity_idx   ON events (severity,   occurred_at DESC);
CREATE INDEX events_code_idx       ON events (code,       occurred_at DESC);

-- Create monthly partitions: 2025-01 through 2026-12.
-- The partition manager goroutine extends these as time passes.
DO $$
DECLARE
    d DATE := '2025-01-01';
BEGIN
    WHILE d < '2027-01-01' LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS events_%s
             PARTITION OF events
             FOR VALUES FROM (%L) TO (%L)',
            to_char(d, 'YYYY_MM'),
            d,
            d + INTERVAL '1 month'
        );
        d := d + INTERVAL '1 month';
    END LOOP;
END;
$$;

-- Default partition catches any rows outside the pre-created range.
CREATE TABLE IF NOT EXISTS events_default
    PARTITION OF events DEFAULT;

-- ──────────────────────────────────────────────────────────────────────────────
-- node_heartbeats  (partitioned by month)
-- High-volume time-series table storing every agent heartbeat.
-- Separate from events for independent retention (default: 30 days).
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE node_heartbeats (
    id                    UUID        NOT NULL DEFAULT gen_random_uuid(),
    node_id               UUID        NOT NULL REFERENCES nodes(id)    ON DELETE CASCADE,
    cluster_id            UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    load_avg_1            FLOAT,
    load_avg_5            FLOAT,
    mem_used_pct          FLOAT,
    disk_used_pct         FLOAT,
    netbox_running        BOOLEAN,
    rq_running            BOOLEAN,
    redis_running         BOOLEAN,
    sentinel_running      BOOLEAN,
    patroni_running       BOOLEAN,
    postgres_running      BOOLEAN,
    patroni_role          TEXT,
    redis_role            TEXT,
    replication_lag_bytes BIGINT,
    recorded_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, recorded_at)
) PARTITION BY RANGE (recorded_at);

CREATE INDEX heartbeats_node_idx    ON node_heartbeats (node_id,    recorded_at DESC);
CREATE INDEX heartbeats_cluster_idx ON node_heartbeats (cluster_id, recorded_at DESC);

-- Monthly heartbeat partitions: 2025-01 through 2026-06.
DO $$
DECLARE
    d DATE := '2025-01-01';
BEGIN
    WHILE d < '2026-07-01' LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS node_heartbeats_%s
             PARTITION OF node_heartbeats
             FOR VALUES FROM (%L) TO (%L)',
            to_char(d, 'YYYY_MM'),
            d,
            d + INTERVAL '1 month'
        );
        d := d + INTERVAL '1 month';
    END LOOP;
END;
$$;

CREATE TABLE IF NOT EXISTS node_heartbeats_default
    PARTITION OF node_heartbeats DEFAULT;

-- ──────────────────────────────────────────────────────────────────────────────
-- alert_transports  (delivery channels)
-- Created before alert_rules so rules can reference transports via FK.
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE alert_transports (
    id         UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT    NOT NULL UNIQUE,
    type       TEXT    NOT NULL CHECK (type IN ('webhook','email','slack_webhook','slack_bot')),
    -- Config fields by type:
    -- webhook:       { "url", "method"?, "headers"?, "body_template"? }
    -- email:         { "smtp_host", "smtp_port", "smtp_tls", "smtp_user"?, "smtp_pass_enc"?, "smtp_from", "to" }
    -- slack_webhook: { "url" }
    -- slack_bot:     { "token_enc", "channel" }
    config     JSONB   NOT NULL DEFAULT '{}',
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────────────────────────────────────
-- alert_schedules  (named reusable time windows)
-- windows: [{"days":[1,2,3,4,5],"start":"09:00","end":"17:00"}]
-- days: 0=Sunday, 1=Monday ... 6=Saturday  (matches time.Weekday)
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE alert_schedules (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    timezone   TEXT NOT NULL DEFAULT 'UTC',
    windows    JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────────────────────────────────────
-- alert_rules
-- LibreNMS-style rules: define what matches, how often to fire, and where to send.
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE alert_rules (
    id          UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,

    -- ── Match conditions (all non-null/non-empty conditions must match) ────────
    -- Empty array = match all
    categories    TEXT[]  NOT NULL DEFAULT '{}',
    codes         TEXT[]  NOT NULL DEFAULT '{}',  -- prefix or exact, e.g. 'NBC-HA'
    min_severity  TEXT    NOT NULL DEFAULT 'info'
                      CHECK (min_severity IN ('debug','info','warn','error','critical')),
    message_regex TEXT,   -- optional POSIX regex matched against message

    -- ── Metric threshold (matches latest heartbeat for the triggering node) ────
    -- metric_field: 'disk_used_pct' | 'load_avg_1' | 'mem_used_pct' | 'replication_lag_bytes'
    metric_field    TEXT,
    metric_operator TEXT CHECK (metric_operator IN ('>','>=','<','<=','==')),
    metric_value    FLOAT,

    -- ── Scope (null = all clusters/nodes) ─────────────────────────────────────
    cluster_id UUID REFERENCES clusters(id) ON DELETE CASCADE,
    node_id    UUID REFERENCES nodes(id)    ON DELETE CASCADE,

    -- ── Re-alert behavior ─────────────────────────────────────────────────────
    -- 'once':             fire once, silence until resolved and re-triggered
    -- 're_alert':         re-send after re_alert_mins while still active
    -- 'every_occurrence': fire on every matching event regardless of state
    fire_mode       TEXT    NOT NULL DEFAULT 'once'
                        CHECK (fire_mode IN ('once','re_alert','every_occurrence')),
    re_alert_mins   INT,    -- only used when fire_mode = 're_alert'
    max_re_alerts   INT,    -- NULL = unlimited re-alerts
    notify_on_clear BOOLEAN NOT NULL DEFAULT TRUE,

    -- ── Escalation ────────────────────────────────────────────────────────────
    escalate_after_mins   INT,
    escalate_transport_id UUID REFERENCES alert_transports(id) ON DELETE SET NULL,

    -- ── Schedule (optional) ───────────────────────────────────────────────────
    schedule_id UUID REFERENCES alert_schedules(id) ON DELETE SET NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX alert_rules_cluster_idx ON alert_rules (cluster_id) WHERE cluster_id IS NOT NULL;
CREATE INDEX alert_rules_node_idx    ON alert_rules (node_id)    WHERE node_id    IS NOT NULL;

-- ──────────────────────────────────────────────────────────────────────────────
-- alert_rule_transports  (many-to-many: rules → transports)
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE alert_rule_transports (
    rule_id      UUID NOT NULL REFERENCES alert_rules(id)      ON DELETE CASCADE,
    transport_id UUID NOT NULL REFERENCES alert_transports(id) ON DELETE CASCADE,
    PRIMARY KEY (rule_id, transport_id)
);

-- ──────────────────────────────────────────────────────────────────────────────
-- active_alert_states  (state machine for each rule × scope)
-- One row per active rule+cluster+node combination.
-- Unique index prevents duplicate active alerts for the same rule+scope.
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE active_alert_states (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id         UUID        NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    cluster_id      UUID        REFERENCES clusters(id) ON DELETE CASCADE,
    node_id         UUID        REFERENCES nodes(id)    ON DELETE SET NULL,
    state           TEXT        NOT NULL DEFAULT 'active'
                        CHECK (state IN ('active','resolved','acknowledged')),
    re_alert_count  INT         NOT NULL DEFAULT 0,
    escalated       BOOLEAN     NOT NULL DEFAULT FALSE,
    first_fired_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_fired_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_alerted_at TIMESTAMPTZ,
    resolved_at     TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID        REFERENCES users(id) ON DELETE SET NULL
);

-- Prevent duplicate active alert states for the same rule+scope.
-- Use COALESCE so nulls compare as equal.
CREATE UNIQUE INDEX active_alert_states_unique_idx ON active_alert_states (
    rule_id,
    COALESCE(cluster_id, '00000000-0000-0000-0000-000000000000'),
    COALESCE(node_id,    '00000000-0000-0000-0000-000000000000')
) WHERE state != 'resolved';

CREATE INDEX active_alert_states_active_idx ON active_alert_states (state)
    WHERE state = 'active';

-- ──────────────────────────────────────────────────────────────────────────────
-- syslog_destinations
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE syslog_destinations (
    id           UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT    NOT NULL UNIQUE,
    protocol     TEXT    NOT NULL CHECK (protocol IN ('udp','tcp','tcp+tls')),
    host         TEXT    NOT NULL,
    port         INT     NOT NULL DEFAULT 514,
    tls_ca_cert  TEXT,           -- PEM-encoded CA certificate for TLS verification
    -- categories: empty array = forward all categories
    categories   TEXT[]  NOT NULL DEFAULT '{}',
    min_severity TEXT    NOT NULL DEFAULT 'info'
                     CHECK (min_severity IN ('debug','info','warn','error','critical')),
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────────────────────────────────────
-- event_retention  (per-category retention configuration)
-- The partition manager uses these to drop old partitions.
-- ──────────────────────────────────────────────────────────────────────────────
CREATE TABLE event_retention (
    category    TEXT PRIMARY KEY,
    retain_days INT  NOT NULL DEFAULT 90 CHECK (retain_days > 0),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO event_retention (category, retain_days) VALUES
    ('cluster',   365),
    ('ha',        365),
    ('config',    365),
    ('service',    90),
    ('agent',      90),
    ('heartbeat',  30)
ON CONFLICT (category) DO NOTHING;
