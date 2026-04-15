CREATE TABLE failover_events (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id       UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    -- 'failover' | 'failback' | 'maintenance_failover'
    event_type       TEXT        NOT NULL,
    -- what triggered it: 'disconnect' | 'heartbeat' | 'maintenance'
    trigger          TEXT        NOT NULL,
    failed_node_id   UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    failed_node_name TEXT,
    target_node_id   UUID        REFERENCES nodes(id) ON DELETE SET NULL,
    target_node_name TEXT,
    success          BOOLEAN     NOT NULL,
    reason           TEXT,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX failover_events_cluster_idx ON failover_events(cluster_id, occurred_at DESC);
