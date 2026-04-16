-- 000001_initial.up.sql

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ──────────────────────────────────────────────
-- users
-- ──────────────────────────────────────────────
CREATE TABLE users (
    id              UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    username        TEXT    NOT NULL UNIQUE,
    password_hash   TEXT    NOT NULL,
    role            TEXT    NOT NULL DEFAULT 'operator'
                                CHECK (role IN ('admin', 'operator', 'viewer')),
    totp_secret_enc BYTEA,
    totp_enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at   TIMESTAMPTZ
);

-- ──────────────────────────────────────────────
-- refresh_tokens
-- ──────────────────────────────────────────────
CREATE TABLE refresh_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    issued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX ON refresh_tokens (user_id);

-- ──────────────────────────────────────────────
-- clusters
-- ──────────────────────────────────────────────
CREATE TABLE clusters (
    id                         UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    name                       TEXT    NOT NULL UNIQUE,
    mode                       TEXT    NOT NULL CHECK (mode IN ('active_standby', 'ha')),
    auto_failover              BOOLEAN NOT NULL DEFAULT TRUE,
    auto_failback              BOOLEAN NOT NULL DEFAULT TRUE,
    app_tier_always_available  BOOLEAN NOT NULL DEFAULT FALSE,
    failover_on_maintenance    BOOLEAN NOT NULL DEFAULT TRUE,
    failover_delay_secs        INTEGER NOT NULL DEFAULT 30,
    vip                        INET,
    patroni_scope              TEXT    NOT NULL,
    netbox_version             TEXT    NOT NULL DEFAULT '4.x',
    netbox_secret_key          BYTEA   NOT NULL,
    api_token_pepper           BYTEA   NOT NULL,
    media_sync_enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    extra_folders_sync_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    extra_sync_folders         TEXT[]  NOT NULL DEFAULT '{}',
    patroni_configured         BOOLEAN NOT NULL DEFAULT FALSE,
    redis_sentinel_master      TEXT    NOT NULL DEFAULT 'netbox',
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────
-- nodes
-- ──────────────────────────────────────────────
CREATE TABLE nodes (
    id                  UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id          UUID    NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    hostname            TEXT    NOT NULL,
    ip_address          INET    NOT NULL,
    role                TEXT    NOT NULL CHECK (role IN ('hyperconverged', 'app', 'db_only')),
    failover_priority   INT     NOT NULL DEFAULT 50,
    agent_status        TEXT    NOT NULL DEFAULT 'unknown'
                                    CHECK (agent_status IN ('connected', 'disconnected', 'unknown')),
    last_seen_at        TIMESTAMPTZ,
    patroni_state       JSONB,
    netbox_running      BOOLEAN,
    rq_running          BOOLEAN,
    suppress_auto_start BOOLEAN NOT NULL DEFAULT FALSE,
    maintenance_mode    BOOLEAN NOT NULL DEFAULT FALSE,
    ssh_port            INT     NOT NULL DEFAULT 22,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, hostname)
);
CREATE INDEX ON nodes (cluster_id);

-- ──────────────────────────────────────────────
-- agent_tokens
-- ──────────────────────────────────────────────
CREATE TABLE agent_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id      UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    issued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);
CREATE INDEX ON agent_tokens (node_id);

-- One-time, short-lived tokens used before a node has a permanent token.
CREATE TABLE registration_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id    UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    issued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

-- ──────────────────────────────────────────────
-- staging (unassigned agents)
-- ──────────────────────────────────────────────
CREATE TABLE staging_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash TEXT NOT NULL UNIQUE,
    label      TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE TABLE staging_agents (
    id            UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname      TEXT    NOT NULL,
    ip_address    TEXT,
    os            TEXT,
    arch          TEXT,
    agent_version TEXT,
    token_hash    TEXT    NOT NULL UNIQUE,
    status        TEXT    NOT NULL DEFAULT 'connected'
                              CHECK (status IN ('connected', 'disconnected')),
    last_seen_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────
-- credentials
-- ──────────────────────────────────────────────
CREATE TABLE credentials (
    id           UUID  PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id   UUID  NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    kind         TEXT  NOT NULL CHECK (kind IN (
                     'postgres_superuser',
                     'postgres_replication',
                     'netbox_db_user',
                     'redis_password',
                     'patroni_rest_password'
                 )),
    username     TEXT  NOT NULL,
    password_enc BYTEA NOT NULL,
    db_name      TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at   TIMESTAMPTZ,
    UNIQUE (cluster_id, kind)
);

-- ──────────────────────────────────────────────
-- netbox_configs
-- ──────────────────────────────────────────────
CREATE TABLE netbox_configs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id      UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    version         INT  NOT NULL DEFAULT 1,
    config_template TEXT NOT NULL,
    rendered_hash   TEXT,
    pushed_at       TIMESTAMPTZ,
    push_status     TEXT CHECK (push_status IN ('pending', 'in_progress', 'success', 'partial', 'failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, version)
);

CREATE TABLE netbox_config_overrides (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    config_id UUID NOT NULL REFERENCES netbox_configs(id) ON DELETE CASCADE,
    node_id   UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    key       TEXT NOT NULL,
    value     TEXT NOT NULL,
    UNIQUE (config_id, node_id, key)
);

-- ──────────────────────────────────────────────
-- retention_policies
-- ──────────────────────────────────────────────
CREATE TABLE retention_policies (
    cluster_id     UUID PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    retention_days INT  NOT NULL DEFAULT 7 CHECK (retention_days > 0),
    expire_cmd     TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ──────────────────────────────────────────────
-- task_results
-- ──────────────────────────────────────────────
CREATE TABLE task_results (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id          UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    task_id          UUID NOT NULL UNIQUE,
    task_type        TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'queued'
                         CHECK (status IN ('queued', 'sent', 'ack', 'success', 'failure', 'timeout')),
    request_payload  JSONB,
    response_payload JSONB,
    queued_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ
);
CREATE INDEX ON task_results (node_id, queued_at DESC);
CREATE INDEX ON task_results (status) WHERE status IN ('queued', 'sent', 'ack');

-- ──────────────────────────────────────────────
-- failover_events
-- ──────────────────────────────────────────────
CREATE TABLE failover_events (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id       UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    event_type       TEXT        NOT NULL,  -- 'failover' | 'failback' | 'maintenance_failover'
    trigger          TEXT        NOT NULL,  -- 'disconnect' | 'heartbeat' | 'maintenance'
    failed_node_id   UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    failed_node_name TEXT,
    target_node_id   UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    target_node_name TEXT,
    success          BOOLEAN     NOT NULL,
    reason           TEXT,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX failover_events_cluster_idx ON failover_events(cluster_id, occurred_at DESC);

-- ──────────────────────────────────────────────
-- node_log_entries
-- ──────────────────────────────────────────────
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
CREATE INDEX node_log_entries_cluster_idx ON node_log_entries(cluster_id, occurred_at DESC);
CREATE INDEX node_log_entries_node_idx    ON node_log_entries(node_id, occurred_at DESC);

-- ──────────────────────────────────────────────
-- alerts
-- ──────────────────────────────────────────────
CREATE TABLE alert_configs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    type        TEXT        NOT NULL,  -- 'webhook' | 'email'
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
    severity        TEXT        NOT NULL,  -- 'warn' | 'error'
    condition       TEXT        NOT NULL,  -- 'agent_disconnected' | 'netbox_down' | 'rq_down'
    message         TEXT        NOT NULL,
    fired_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ
);
CREATE INDEX active_alerts_cluster_idx    ON active_alerts(cluster_id, fired_at DESC);
CREATE INDEX active_alerts_unresolved_idx ON active_alerts(resolved_at) WHERE resolved_at IS NULL;

-- ──────────────────────────────────────────────
-- audit_logs
-- ──────────────────────────────────────────────
CREATE TABLE audit_logs (
    id                  BIGSERIAL PRIMARY KEY,
    actor_user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_agent_node_id UUID REFERENCES nodes(id) ON DELETE SET NULL,
    action              TEXT NOT NULL,
    target_type         TEXT,
    target_id           UUID,
    detail              JSONB,
    outcome             TEXT CHECK (outcome IN ('success', 'failure', 'pending')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON audit_logs (created_at DESC);
CREATE INDEX ON audit_logs (target_id);
CREATE INDEX ON audit_logs (actor_user_id);
