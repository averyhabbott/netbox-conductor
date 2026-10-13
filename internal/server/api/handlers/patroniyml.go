package handlers

import (
	"context"
	"slices"

	"github.com/averyhabbott/netbox-conductor/internal/server/configgen"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/nodestate"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// Every patroni.yml the conductor writes is built here, so Configure Failover
// and the single-node push can't drift apart.

// patroniClusterInput returns the cluster-wide part of patroni.yml: scope and
// credentials. The caller adds WitnessAddr; renderPatroniFor adds the node's own fields.
func patroniClusterInput(ctx context.Context, creds *queries.CredentialQuerier, enc *crypto.Encryptor, cluster *queries.Cluster) configgen.PatroniInput {
	in := configgen.PatroniInput{Scope: cluster.PatroniScope}
	in.DBSuperUser, in.DBSuperPass = creds.GetDecrypted(ctx, enc, cluster.ID, CredKindPostgresSuperuser, CredDefaultUserPostgresSuperuser)
	in.DBReplicaUser, in.DBReplicaPass = creds.GetDecrypted(ctx, enc, cluster.ID, CredKindPostgresReplication, CredDefaultUserPostgresReplication)
	in.RESTUsername, in.RESTPassword = creds.GetDecrypted(ctx, enc, cluster.ID, CredKindPatroniREST, CredDefaultUserPatroniREST)
	return in
}

// patroniRaftPeers returns the Raft address of every database node; app-only
// nodes don't run Patroni.
func patroniRaftPeers(nodes []queries.Node) []string {
	peers := make([]string, 0, len(nodes))
	for i := range nodes {
		if nodestate.IsDBRole(nodes[i].Role) {
			peers = append(peers, stripCIDR(nodes[i].IPAddress)+":"+protocol.PatroniRaftPortStr)
		}
	}
	return peers
}

// renderPatroniFor renders patroni.yml for one node from the cluster-wide input.
func renderPatroniFor(cluster configgen.PatroniInput, raftPeers []string, node queries.Node) (string, error) {
	in := cluster
	in.NodeName = node.Hostname
	in.NodeAddr = stripCIDR(node.IPAddress)
	in.RaftSelfAddr = in.NodeAddr + ":" + protocol.PatroniRaftPortStr
	in.RaftPartners = slices.DeleteFunc(slices.Clone(raftPeers), func(p string) bool { return p == in.RaftSelfAddr })
	in.Priority = node.FailoverPriority
	return configgen.RenderPatroni(in)
}
