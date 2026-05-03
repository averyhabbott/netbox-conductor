CREATE TABLE witness_ports (
    cluster_id  UUID    PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    port        INT     NOT NULL UNIQUE
);
