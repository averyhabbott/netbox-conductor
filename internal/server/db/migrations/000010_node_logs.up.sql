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
