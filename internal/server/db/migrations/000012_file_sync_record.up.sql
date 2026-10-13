-- Two-way file sync (media and extra folders). Folder '' is NETBOX_MEDIA_ROOT.
-- file_sync_nodes/file_sync_files: each node's files as of its last successful
-- sync, so the next sync can tell what was added, changed or deleted there.
-- file_sync_deletes: files deleted on some node, kept until every app-tier and
-- hyperconverged node in the cluster has synced since.
CREATE TABLE file_sync_nodes (
    cluster_id UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    folder     TEXT        NOT NULL,
    node_id    UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    synced_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (cluster_id, folder, node_id)
);

CREATE TABLE file_sync_files (
    cluster_id UUID NOT NULL,
    folder     TEXT NOT NULL,
    node_id    UUID NOT NULL,
    path       TEXT NOT NULL,
    sha256     TEXT NOT NULL,
    PRIMARY KEY (cluster_id, folder, node_id, path),
    FOREIGN KEY (cluster_id, folder, node_id) REFERENCES file_sync_nodes ON DELETE CASCADE
);

CREATE TABLE file_sync_deletes (
    cluster_id UUID        NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    folder     TEXT        NOT NULL,
    path       TEXT        NOT NULL,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cluster_id, folder, path)
);
