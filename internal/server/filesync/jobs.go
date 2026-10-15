package filesync

import (
	"errors"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/nodestate"
	"github.com/google/uuid"
)

// BackupJob syncs a local-disk storage location's pgBackRest repo from the
// main server (source) to dests: see backupPlan. Files are written as postgres.
func BackupJob(target queries.BackupTarget, stanza string, source queries.Node, dests []queries.Node) Job {
	return Job{
		ClusterID: target.ClusterID, Name: "backups (" + target.Label + ")", Root: *target.PosixPath,
		Nodes: append([]queries.Node{source}, dests...), Source: source.ID,
		Backup: &Backup{Target: target, Stanza: stanza},
	}
}

// MediaJobs returns one two-way job for the media root and one per extra sync
// folder, across the connected app-tier and hyperconverged nodes.
// Group-writable modes let the netbox user's ACL entry
// (check-sync-permissions) modify what the agent copied in.
func MediaJobs(cluster *queries.Cluster, nodes []queries.Node, connected func(uuid.UUID) bool) ([]Job, error) {
	var peers []queries.Node
	for _, n := range nodes {
		if nodestate.IsAppRole(n.Role) && connected(n.ID) {
			peers = append(peers, n)
		}
	}
	if len(peers) < 2 {
		return nil, errors.New("file sync needs at least two connected app-tier or hyperconverged nodes")
	}

	jobs := make([]Job, 0, 1+len(cluster.ExtraSyncFolders))
	for _, f := range SyncFolders(cluster) {
		name := f
		if f == "" {
			name = "media"
		}
		jobs = append(jobs, Job{
			ClusterID: cluster.ID, Name: name, Root: f, Nodes: peers,
			FileMode: 0o664, DirMode: 0o775,
		})
	}
	return jobs, nil
}

// SyncFolders returns the folders a cluster syncs two ways: "" (each node's
// NetBox media folder) and, if enabled, the extra sync folders.
func SyncFolders(cluster *queries.Cluster) []string {
	folders := []string{""}
	if cluster.ExtraFoldersSyncEnabled {
		for _, f := range cluster.ExtraSyncFolders {
			if f != "" {
				folders = append(folders, f)
			}
		}
	}
	return folders
}
