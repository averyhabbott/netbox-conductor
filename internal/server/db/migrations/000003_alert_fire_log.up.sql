-- 000003_alert_fire_log.up.sql
--
-- Adds a denormalized history table that records every alert delivery.
-- Names (rule, transport, cluster, node) are copied at write time so
-- the record survives subsequent renames or deletions.

CREATE TABLE alert_fire_log (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id        UUID        REFERENCES alert_rules(id)      ON DELETE SET NULL,
    rule_name      TEXT        NOT NULL,
    transport_id   UUID        REFERENCES alert_transports(id) ON DELETE SET NULL,
    transport_name TEXT        NOT NULL,
    transport_type TEXT        NOT NULL,
    cluster_id     UUID        REFERENCES clusters(id)         ON DELETE SET NULL,
    cluster_name   TEXT,
    node_id        UUID        REFERENCES nodes(id)            ON DELETE SET NULL,
    node_name      TEXT,
    event_code     TEXT        NOT NULL,
    event_message  TEXT        NOT NULL,
    event_severity TEXT        NOT NULL,
    is_resolve     BOOLEAN     NOT NULL DEFAULT FALSE,
    fired_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON alert_fire_log (fired_at DESC);
CREATE INDEX ON alert_fire_log (rule_id);
