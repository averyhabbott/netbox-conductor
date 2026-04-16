CREATE TABLE alert_configs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    type        TEXT        NOT NULL,     -- 'webhook' | 'email'
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
    severity        TEXT        NOT NULL,   -- 'warn' | 'error'
    condition       TEXT        NOT NULL,   -- 'agent_disconnected' | 'netbox_down' | 'rq_down'
    message         TEXT        NOT NULL,
    fired_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ
);

CREATE INDEX active_alerts_cluster_idx    ON active_alerts(cluster_id, fired_at DESC);
CREATE INDEX active_alerts_unresolved_idx ON active_alerts(resolved_at) WHERE resolved_at IS NULL;
