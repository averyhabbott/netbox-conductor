package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/abottVU/netbox-failover/internal/server/configgen"
	"github.com/abottVU/netbox-failover/internal/server/crypto"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/abottVU/netbox-failover/internal/server/hub"
	"github.com/abottVU/netbox-failover/internal/server/patroni"
	"github.com/abottVU/netbox-failover/internal/shared/protocol"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// PatroniHandler handles Patroni topology queries and config push.
type PatroniHandler struct {
	clusters   *queries.ClusterQuerier
	nodes      *queries.NodeQuerier
	creds      *queries.CredentialQuerier
	taskResults *queries.TaskResultQuerier
	enc        *crypto.Encryptor
	dispatcher *hub.Dispatcher
	witness    *patroni.WitnessManager
}

func NewPatroniHandler(
	clusters *queries.ClusterQuerier,
	nodes *queries.NodeQuerier,
	creds *queries.CredentialQuerier,
	taskResults *queries.TaskResultQuerier,
	enc *crypto.Encryptor,
	dispatcher *hub.Dispatcher,
	witness *patroni.WitnessManager,
) *PatroniHandler {
	return &PatroniHandler{
		clusters:    clusters,
		nodes:       nodes,
		creds:       creds,
		taskResults: taskResults,
		enc:         enc,
		dispatcher:  dispatcher,
		witness:     witness,
	}
}

// Topology returns the live Patroni state for each node in a cluster.
// GET /api/v1/clusters/:id/patroni/topology
func (h *PatroniHandler) Topology(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	nodes, err := h.nodes.ListByCluster(c.Request().Context(), clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	type nodeTopology struct {
		NodeID      string          `json:"node_id"`
		Hostname    string          `json:"hostname"`
		Role        string          `json:"role"`
		AgentStatus string          `json:"agent_status"`
		PatroniRole string          `json:"patroni_role"`
		PatroniState json.RawMessage `json:"patroni_state,omitempty"`
	}

	result := make([]nodeTopology, 0, len(nodes))
	for _, n := range nodes {
		nt := nodeTopology{
			NodeID:      n.ID.String(),
			Hostname:    n.Hostname,
			Role:        n.Role,
			AgentStatus: n.AgentStatus,
		}
		if n.PatroniState != nil {
			var ps map[string]any
			if err := json.Unmarshal(n.PatroniState, &ps); err == nil {
				if role, ok := ps["role"].(string); ok {
					nt.PatroniRole = role
				}
			}
			nt.PatroniState = n.PatroniState
		}
		result = append(result, nt)
	}

	witnessAddr := ""
	if h.witness != nil {
		witnessAddr = h.witness.Addr(clusterID)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":   clusterID.String(),
		"witness_addr": witnessAddr,
		"nodes":        result,
	})
}

// PushPatroniConfig renders and pushes patroni.yml to all DB-role nodes.
// POST /api/v1/clusters/:id/patroni/push-config
func (h *PatroniHandler) PushPatroniConfig(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	ctx := c.Request().Context()

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	// Gather credentials
	superUser, superPass := "postgres", ""
	replicaUser, replicaPass := "replicator", ""
	restUser, restPass := "patroni", ""

	if cred, err := h.creds.GetByKind(ctx, clusterID, "postgres_superuser"); err == nil {
		superUser = cred.Username
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			superPass = string(pw)
		}
	}
	if cred, err := h.creds.GetByKind(ctx, clusterID, "postgres_replication"); err == nil {
		replicaUser = cred.Username
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			replicaPass = string(pw)
		}
	}
	if cred, err := h.creds.GetByKind(ctx, clusterID, "patroni_rest_password"); err == nil {
		restUser = cred.Username
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			restPass = string(pw)
		}
	}

	// Build Raft partner list (all nodes with patroni roles)
	raftPeers := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Role == "hyperconverged" || n.Role == "db_only" {
			raftPeers = append(raftPeers, stripCIDR(n.IPAddress)+":5433")
		}
	}

	witnessAddr := ""
	if h.witness != nil {
		witnessAddr = h.witness.Addr(clusterID)
	}

	type nodeResult struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	results := make([]nodeResult, 0)

	for _, node := range nodes {
		if node.Role != "hyperconverged" && node.Role != "db_only" {
			continue // only push to nodes that run Patroni
		}

		nodeIP := stripCIDR(node.IPAddress)

		// Build per-node partner list (exclude self)
		partners := make([]string, 0)
		for _, p := range raftPeers {
			if !strings.HasPrefix(p, nodeIP+":") {
				partners = append(partners, p)
			}
		}

		content, err := configgen.RenderPatroni(configgen.PatroniInput{
			Scope:         cluster.PatroniScope,
			NodeName:      node.Hostname,
			NodeAddr:      nodeIP,
			RaftSelfAddr:  nodeIP + ":5433",
			RaftPartners:  partners,
			WitnessAddr:   witnessAddr,
			RESTUsername:  restUser,
			RESTPassword:  restPass,
			DBSuperUser:   superUser,
			DBSuperPass:   superPass,
			DBReplicaUser: replicaUser,
			DBReplicaPass: replicaPass,
		})
		if err != nil {
			results = append(results, nodeResult{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "error", Error: err.Error(),
			})
			continue
		}

		taskID := uuid.New()
		params, _ := json.Marshal(protocol.PatroniConfigWriteParams{
			Content:      content,
			RestartAfter: false,
		})

		_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskWritePatroniConf), params)

		if err := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskWritePatroniConf,
			Params:      json.RawMessage(params),
			TimeoutSecs: 30,
		}); err != nil {
			results = append(results, nodeResult{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "offline", Error: err.Error(),
			})
			continue
		}

		_ = h.taskResults.SetSent(ctx, taskID)
		results = append(results, nodeResult{
			NodeID: node.ID.String(), Hostname: node.Hostname,
			TaskID: taskID.String(), Status: "dispatched",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": clusterID.String(),
		"nodes":      results,
	})
}

// Switchover triggers a Patroni switchover (planned, graceful).
// POST /api/v1/clusters/:id/patroni/switchover
func (h *PatroniHandler) Switchover(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		Candidate string `json:"candidate"` // hostname or node_id of desired new primary; empty = let Patroni choose
	}
	_ = c.Bind(&req)

	ctx := c.Request().Context()
	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	// Find primary (patroni_state.role == "primary")
	var primaryNode *queries.Node
	for i := range nodes {
		if nodes[i].PatroniState != nil {
			var ps map[string]any
			if err := json.Unmarshal(nodes[i].PatroniState, &ps); err == nil {
				if ps["role"] == "primary" {
					primaryNode = &nodes[i]
					break
				}
			}
		}
	}

	if primaryNode == nil {
		return echo.NewHTTPError(http.StatusConflict, "no primary node found in cluster")
	}

	// Dispatch exec.run task to primary to run patronictl switchover
	args := []string{"--master", primaryNode.Hostname}
	if req.Candidate != "" {
		args = append(args, "--candidate", req.Candidate)
	}
	args = append(args, "--force")

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.RunCommandParams{
		Command: "patronictl",
		Args:    append([]string{"-c", "/etc/patroni/patroni.yml", "switchover"}, args...),
	})

	_ = h.taskResults.Create(ctx, primaryNode.ID, taskID, string(protocol.TaskRunCommand), params)

	if err := h.dispatcher.Dispatch(primaryNode.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskRunCommand,
		Params:      json.RawMessage(params),
		TimeoutSecs: 30,
	}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "primary node is not connected: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)
	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":       taskID.String(),
		"primary_node":  primaryNode.Hostname,
		"candidate":     req.Candidate,
	})
}

// StartWitness starts the Patroni witness for a cluster (active_standby only).
// POST /api/v1/clusters/:id/patroni/witness/start
func (h *PatroniHandler) StartWitness(c echo.Context) error {
	if h.witness == nil {
		return echo.NewHTTPError(http.StatusNotImplemented, "witness manager not configured")
	}
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	cluster, err := h.clusters.GetByID(c.Request().Context(), clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}
	if cluster.Mode != "active_standby" {
		return echo.NewHTTPError(http.StatusBadRequest, "witness only applies to active_standby clusters")
	}

	nodes, err := h.nodes.ListByCluster(c.Request().Context(), clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	partners := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Role == "hyperconverged" || n.Role == "db_only" {
			partners = append(partners, stripCIDR(n.IPAddress)+":5433")
		}
	}

	if err := h.witness.Start(clusterID, partners); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to start witness: "+err.Error())
	}

	return c.JSON(http.StatusOK, map[string]any{
		"witness_addr": h.witness.Addr(clusterID),
		"partners":     partners,
	})
}
