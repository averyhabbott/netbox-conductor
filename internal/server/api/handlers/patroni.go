package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/configgen"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/patroni"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// PatroniHandler handles Patroni topology queries and config push.
type PatroniHandler struct {
	clusters       *queries.ClusterQuerier
	nodes          *queries.NodeQuerier
	creds          *queries.CredentialQuerier
	taskResults    *queries.TaskResultQuerier
	retention      *queries.RetentionQuerier
	failoverEvents *queries.FailoverEventQuerier
	enc            *crypto.Encryptor
	dispatcher     *hub.Dispatcher
	witness        *patroni.WitnessManager
}

func NewPatroniHandler(
	clusters *queries.ClusterQuerier,
	nodes *queries.NodeQuerier,
	creds *queries.CredentialQuerier,
	taskResults *queries.TaskResultQuerier,
	retention *queries.RetentionQuerier,
	failoverEvents *queries.FailoverEventQuerier,
	enc *crypto.Encryptor,
	dispatcher *hub.Dispatcher,
	witness *patroni.WitnessManager,
) *PatroniHandler {
	return &PatroniHandler{
		clusters:       clusters,
		nodes:          nodes,
		creds:          creds,
		taskResults:    taskResults,
		retention:      retention,
		failoverEvents: failoverEvents,
		enc:            enc,
		dispatcher:     dispatcher,
		witness:        witness,
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
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

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

// History returns Patroni-related task history across all nodes in a cluster.
// GET /api/v1/clusters/:id/patroni/history
func (h *PatroniHandler) History(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	ctx := c.Request().Context()
	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	nodeIDs := make([]uuid.UUID, len(nodes))
	hostnameByID := make(map[string]string, len(nodes))
	for i, n := range nodes {
		nodeIDs[i] = n.ID
		hostnameByID[n.ID.String()] = n.Hostname
	}

	tasks, err := h.taskResults.ListByNodeIDs(ctx, nodeIDs, 100)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list tasks")
	}

	patroniTypes := map[string]bool{
		"patroni.write_config":    true,
		"patroni.install":         true,
		"service.restart.patroni": true,
		"exec.run":                true, // switchover uses exec.run
	}

	type historyRow struct {
		TaskID      string  `json:"task_id"`
		NodeID      string  `json:"node_id"`
		Hostname    string  `json:"hostname"`
		TaskType    string  `json:"task_type"`
		Status      string  `json:"status"`
		QueuedAt    string  `json:"queued_at"`
		CompletedAt *string `json:"completed_at,omitempty"`
	}

	rows := make([]historyRow, 0)
	for _, t := range tasks {
		if !patroniTypes[t.TaskType] {
			continue
		}
		row := historyRow{
			TaskID:   t.TaskID.String(),
			NodeID:   t.NodeID.String(),
			Hostname: hostnameByID[t.NodeID.String()],
			TaskType: t.TaskType,
			Status:   t.Status,
			QueuedAt: t.QueuedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if t.CompletedAt != nil {
			s := t.CompletedAt.UTC().Format("2006-01-02T15:04:05Z")
			row.CompletedAt = &s
		}
		rows = append(rows, row)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": clusterID.String(),
		"history":    rows,
	})
}

// PushPatroniConfigNode renders and pushes patroni.yml to a single node.
// POST /api/v1/clusters/:id/nodes/:nid/push-patroni-config
func (h *PatroniHandler) PushPatroniConfigNode(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	ctx := c.Request().Context()
	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	node, err := h.nodes.GetByID(ctx, nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	if node.Role != "hyperconverged" && node.Role != "db_only" {
		return echo.NewHTTPError(http.StatusBadRequest, "node does not run Patroni (role must be hyperconverged or db_only)")
	}

	allNodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

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

	nodeIP := stripCIDR(node.IPAddress)
	allPeers := make([]string, 0)
	for _, n := range allNodes {
		if n.Role == "hyperconverged" || n.Role == "db_only" {
			allPeers = append(allPeers, stripCIDR(n.IPAddress)+":5433")
		}
	}
	partners := make([]string, 0)
	for _, p := range allPeers {
		if !strings.HasPrefix(p, nodeIP+":") {
			partners = append(partners, p)
		}
	}

	witnessAddr := ""
	if h.witness != nil {
		witnessAddr = h.witness.Addr(clusterID)
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
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to render patroni config: "+err.Error())
	}

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.PatroniConfigWriteParams{Content: content, RestartAfter: false})
	_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskWritePatroniConf), params)

	if err := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskWritePatroniConf,
		Params:      json.RawMessage(params),
		TimeoutSecs: 30,
	}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "node is not connected: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)
	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":  taskID.String(),
		"node_id":  node.ID.String(),
		"hostname": node.Hostname,
		"status":   "dispatched",
	})
}

// DBRestore dispatches a database restore task to a specific node.
// POST /api/v1/clusters/:id/nodes/:nid/db-restore
func (h *PatroniHandler) DBRestore(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	var req struct {
		Method     string `json:"method"`       // "reinitialize" | "pitr"
		TargetTime string `json:"target_time"`  // required for pitr
		RestoreCmd string `json:"restore_cmd"`  // optional override
	}
	if err := c.Bind(&req); err != nil || req.Method == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "method is required (reinitialize or pitr)")
	}

	ctx := c.Request().Context()

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	node, err := h.nodes.GetByID(ctx, nid)
	if err != nil || node.ClusterID != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "node not found in cluster")
	}

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.DBRestoreParams{
		Method:       req.Method,
		TargetTime:   req.TargetTime,
		RestoreCmd:   req.RestoreCmd,
		PatroniScope: cluster.PatroniScope,
	})

	_ = h.taskResults.Create(ctx, nid, taskID, string(protocol.TaskDBRestore), params)

	if err := h.dispatcher.Dispatch(nid, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskDBRestore,
		Params:      json.RawMessage(params),
		TimeoutSecs: 3600,
	}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "node is not connected: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)
	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":  taskID.String(),
		"node_id":  nid.String(),
		"hostname": node.Hostname,
		"method":   req.Method,
		"status":   "dispatched",
	})
}

// PushSentinelConfig renders and dispatches sentinel.conf to all nodes in a cluster.
// POST /api/v1/clusters/:id/sentinel/push-config
func (h *PatroniHandler) PushSentinelConfig(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		RestartAfter bool `json:"restart_after"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
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

	// Load Redis password from credentials (best effort — empty string if not set)
	redisPassword := ""
	if cred, err := h.creds.GetByKind(ctx, clusterID, "redis_password"); err == nil {
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			redisPassword = string(pw)
		}
	}

	// Determine master seed: first node with patroni_state.role == "primary", else first node
	masterHost := ""
	for _, n := range nodes {
		if n.PatroniState != nil {
			var ps map[string]any
			if err := json.Unmarshal(n.PatroniState, &ps); err == nil {
				if ps["role"] == "primary" {
					masterHost = stripCIDR(n.IPAddress)
					break
				}
			}
		}
	}
	if masterHost == "" && len(nodes) > 0 {
		masterHost = stripCIDR(nodes[0].IPAddress)
	}

	type nodeResult struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	results := make([]nodeResult, 0, len(nodes))

	for _, node := range nodes {
		nodeIP := stripCIDR(node.IPAddress)

		content, sha256hex, err := configgen.RenderSentinel(configgen.SentinelInput{
			Scope:      cluster.PatroniScope,
			MasterHost: masterHost,
			BindAddr:   nodeIP,
			Password:   redisPassword,
		})
		if err != nil {
			results = append(results, nodeResult{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "error", Error: err.Error(),
			})
			continue
		}

		taskID := uuid.New()
		params, _ := json.Marshal(protocol.SentinelConfigWriteParams{
			Content:      content,
			Sha256:       sha256hex,
			RestartAfter: req.RestartAfter,
		})

		_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskWriteSentinelConf), params)

		if err := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskWriteSentinelConf,
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
		"cluster_id":  clusterID.String(),
		"master_host": masterHost,
		"nodes":       results,
	})
}

// Failover triggers a forced Patroni failover (used when the primary is unhealthy).
// POST /api/v1/clusters/:id/patroni/failover
func (h *PatroniHandler) Failover(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		Master    string `json:"master"`    // hostname of the dead primary (required)
		Candidate string `json:"candidate"` // desired new primary; empty = let Patroni choose
	}
	if err := c.Bind(&req); err != nil || req.Master == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "master (current/dead primary hostname) is required")
	}

	ctx := c.Request().Context()
	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	// Dispatch to the candidate if specified, otherwise any connected replica.
	var targetNode *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.AgentStatus != "connected" || n.Hostname == req.Master {
			continue
		}
		if req.Candidate != "" && n.Hostname == req.Candidate {
			targetNode = n
			break
		}
		if targetNode == nil {
			targetNode = n
		}
	}
	if targetNode == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected replica found to run patronictl failover")
	}

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	args := []string{"-c", "/etc/patroni/patroni.yml", "failover", cluster.PatroniScope, "--master", req.Master, "--force"}
	if req.Candidate != "" {
		args = append(args, "--candidate", req.Candidate)
	}

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.RunCommandParams{Command: "patronictl", Args: args})
	_ = h.taskResults.Create(ctx, targetNode.ID, taskID, string(protocol.TaskRunCommand), params)

	if err := h.dispatcher.Dispatch(targetNode.ID, protocol.TaskDispatchPayload{
		TaskID: taskID.String(), TaskType: protocol.TaskRunCommand,
		Params: json.RawMessage(params), TimeoutSecs: 30,
	}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "target node is not connected: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)
	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":     taskID.String(),
		"target_node": targetNode.Hostname,
		"master":      req.Master,
		"candidate":   req.Candidate,
	})
}

// InstallPatroni dispatches a Patroni install task to a specific node.
// POST /api/v1/clusters/:id/nodes/:nid/install-patroni
func (h *PatroniHandler) InstallPatroni(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	var req struct {
		PackageManager string `json:"package_manager"`
		InstallCmd     string `json:"install_cmd"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	ctx := c.Request().Context()
	node, err := h.nodes.GetByID(ctx, nid)
	if err != nil || node.ClusterID != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "node not found in cluster")
	}

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.PatroniInstallParams{
		PackageManager: req.PackageManager,
		InstallCmd:     req.InstallCmd,
	})
	_ = h.taskResults.Create(ctx, nid, taskID, string(protocol.TaskInstallPatroni), params)

	if err := h.dispatcher.Dispatch(nid, protocol.TaskDispatchPayload{
		TaskID: taskID.String(), TaskType: protocol.TaskInstallPatroni,
		Params: json.RawMessage(params), TimeoutSecs: 300,
	}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "node is not connected: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)
	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":  taskID.String(),
		"node_id":  nid.String(),
		"hostname": node.Hostname,
		"status":   "dispatched",
	})
}

// GetRetentionPolicy returns the backup retention policy for a cluster.
// GET /api/v1/clusters/:id/retention-policy
func (h *PatroniHandler) GetRetentionPolicy(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	policy, err := h.retention.Get(c.Request().Context(), clusterID)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]any{
			"cluster_id": clusterID.String(), "retention_days": 7, "expire_cmd": "",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":     policy.ClusterID.String(),
		"retention_days": policy.RetentionDays,
		"expire_cmd":     policy.ExpireCmd,
		"updated_at":     policy.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// SetRetentionPolicy creates or updates the backup retention policy for a cluster.
// PUT /api/v1/clusters/:id/retention-policy
func (h *PatroniHandler) SetRetentionPolicy(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	var req struct {
		RetentionDays int    `json:"retention_days"`
		ExpireCmd     string `json:"expire_cmd"`
	}
	if err := c.Bind(&req); err != nil || req.RetentionDays <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "retention_days must be a positive integer")
	}
	policy, err := h.retention.Upsert(c.Request().Context(), clusterID, req.RetentionDays, req.ExpireCmd)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save retention policy")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":     policy.ClusterID.String(),
		"retention_days": policy.RetentionDays,
		"expire_cmd":     policy.ExpireCmd,
		"updated_at":     policy.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// EnforceRetention dispatches a pgbackrest expire task to the primary DB node.
// POST /api/v1/clusters/:id/retention-policy/enforce
func (h *PatroniHandler) EnforceRetention(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()
	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}
	policy, _ := h.retention.Get(ctx, clusterID)

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	var targetNode *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.AgentStatus != "connected" || (n.Role != "hyperconverged" && n.Role != "db_only") {
			continue
		}
		if targetNode == nil {
			targetNode = n
		}
		if n.PatroniState != nil {
			var ps map[string]any
			if err := json.Unmarshal(n.PatroniState, &ps); err == nil && ps["role"] == "primary" {
				targetNode = n
				break
			}
		}
	}
	if targetNode == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected DB node found")
	}

	expireCmd := ""
	if policy != nil {
		expireCmd = policy.ExpireCmd
	}

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.EnforceRetentionParams{
		PatroniScope: cluster.PatroniScope,
		ExpireCmd:    expireCmd,
	})
	_ = h.taskResults.Create(ctx, targetNode.ID, taskID, string(protocol.TaskEnforceRetention), params)

	if err := h.dispatcher.Dispatch(targetNode.ID, protocol.TaskDispatchPayload{
		TaskID: taskID.String(), TaskType: protocol.TaskEnforceRetention,
		Params: json.RawMessage(params), TimeoutSecs: 300,
	}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "target node is not connected: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)
	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":  taskID.String(),
		"node_id":  targetNode.ID.String(),
		"hostname": targetNode.Hostname,
		"status":   "dispatched",
	})
}

// ConfigureFailover is the single-button HA setup endpoint.
// It saves failover settings, auto-generates any missing credentials,
// starts the Patroni witness on the conductor, optionally backs up the
// primary database, pushes patroni.yml + restarts Patroni on all nodes,
// and (when app_tier_always_available=true) pushes Sentinel config.
// POST /api/v1/clusters/:id/configure-failover
func (h *PatroniHandler) ConfigureFailover(c echo.Context) error {
	startedAt := time.Now()

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		AutoFailover           bool    `json:"auto_failover"`
		AutoFailback           bool    `json:"auto_failback"`
		AppTierAlwaysAvailable bool    `json:"app_tier_always_available"`
		FailoverOnMaintenance  bool    `json:"failover_on_maintenance"`
		FailoverDelaySecs      int     `json:"failover_delay_secs"`
		VIP                    *string `json:"vip"`
		RedisSentinelMaster    string  `json:"redis_sentinel_master"`
		SaveBackup             bool    `json:"save_backup"`
		PrimaryNodeID          string  `json:"primary_node_id"` // optional explicit override
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.FailoverDelaySecs <= 0 {
		req.FailoverDelaySecs = 30
	}
	if req.RedisSentinelMaster == "" {
		req.RedisSentinelMaster = "netbox"
	}

	ctx := c.Request().Context()

	slog.Info("configure-failover: request received",
		"cluster", clusterID,
		"auto_failover", req.AutoFailover,
		"app_tier_always_available", req.AppTierAlwaysAvailable,
		"save_backup", req.SaveBackup,
	)

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}
	if cluster.Mode != "active_standby" {
		return echo.NewHTTPError(http.StatusBadRequest, "Configure Failover is only available for active_standby clusters")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}
	if len(nodes) < 2 {
		return echo.NewHTTPError(http.StatusBadRequest, "cluster must have at least 2 nodes before configuring failover")
	}

	// Persist failover settings
	if err := h.clusters.UpdateFailoverSettings(ctx, queries.UpdateClusterParams{
		ID:                     clusterID,
		AutoFailover:           req.AutoFailover,
		AutoFailback:           req.AutoFailback,
		AppTierAlwaysAvailable: req.AppTierAlwaysAvailable,
		FailoverOnMaintenance:  req.FailoverOnMaintenance,
		FailoverDelaySecs:      req.FailoverDelaySecs,
		VIP:                    req.VIP,
		RedisSentinelMaster:    req.RedisSentinelMaster,
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save failover settings")
	}

	// Auto-generate any missing credentials so the operator never has to
	// visit the Credentials tab before clicking Configure Failover.
	warnings := make([]string, 0)
	for _, def := range []struct {
		kind     string
		username string
		dbName   *string
	}{
		{"postgres_superuser", "postgres", nil},
		{"postgres_replication", "replicator", nil},
		{"patroni_rest_password", "patroni", nil},
		{"redis_password", "redis", nil},
	} {
		if _, err := h.creds.GetByKind(ctx, clusterID, def.kind); err != nil {
			raw, genErr := crypto.GenerateToken(32)
			if genErr != nil {
				warnings = append(warnings, fmt.Sprintf("could not generate %s credential: %v", def.kind, genErr))
				continue
			}
			enc, encErr := h.enc.Encrypt([]byte(raw))
			if encErr != nil {
				warnings = append(warnings, fmt.Sprintf("could not encrypt %s credential: %v", def.kind, encErr))
				continue
			}
			if err := h.creds.Upsert(ctx, queries.UpsertCredentialParams{
				ClusterID:   clusterID,
				Kind:        def.kind,
				Username:    def.username,
				PasswordEnc: enc,
				DBName:      def.dbName,
			}); err != nil {
				slog.Warn("configure-failover: failed to store auto-generated credential",
					"cluster", clusterID, "kind", def.kind, "error", err)
				warnings = append(warnings, fmt.Sprintf("failed to store %s credential: %v", def.kind, err))
			}
		}
	}

	// Decrypt credentials for config rendering
	superUser, superPass := "postgres", ""
	replicaUser, replicaPass := "replicator", ""
	restUser, restPass := "patroni", ""
	redisPassword := ""

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
	if cred, err := h.creds.GetByKind(ctx, clusterID, "redis_password"); err == nil {
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			redisPassword = string(pw)
		}
	}

	// Identify primary using the following priority chain:
	//  1. Explicit override — primary_node_id in the request body.
	//  2. Patroni state — a node reporting role=primary.
	//  3. Single netbox_running=true node — unambiguous.
	//  4. Highest failover_priority among connected nodes running NetBox —
	//     used when multiple nodes are running (e.g. before first failover config).
	//     This mirrors the failover manager's tie-break logic.
	var primaryNode *queries.Node

	// (1) Explicit override
	if req.PrimaryNodeID != "" {
		overrideID, parseErr := uuid.Parse(req.PrimaryNodeID)
		if parseErr != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid primary_node_id")
		}
		for i := range nodes {
			if nodes[i].ID == overrideID {
				primaryNode = &nodes[i]
				break
			}
		}
		if primaryNode == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "primary_node_id does not belong to this cluster")
		}
	}

	// (2) Patroni state
	if primaryNode == nil {
		for i := range nodes {
			n := &nodes[i]
			if n.PatroniState != nil {
				var ps map[string]any
				if json.Unmarshal(n.PatroniState, &ps) == nil && ps["role"] == "primary" {
					primaryNode = n
					break
				}
			}
		}
	}

	// (3) + (4) netbox_running — pick best by failover_priority if ambiguous
	if primaryNode == nil {
		for i := range nodes {
			n := &nodes[i]
			if n.NetboxRunning == nil || !*n.NetboxRunning || n.AgentStatus != "connected" {
				continue
			}
			if primaryNode == nil || n.FailoverPriority > primaryNode.FailoverPriority {
				primaryNode = n
			}
		}
	}

	if primaryNode == nil {
		return echo.NewHTTPError(http.StatusConflict,
			"cannot identify primary node: no connected node reports netbox_running=true or patroni role=primary. "+
				"Ensure at least one node is connected and running NetBox.")
	}

	// Build Raft peer list (all nodes participate in Raft consensus)
	raftPeers := make([]string, 0, len(nodes))
	for _, n := range nodes {
		raftPeers = append(raftPeers, stripCIDR(n.IPAddress)+":5433")
	}

	// Start witness on conductor (idempotent — no-op if already running)
	witnessAddr := ""
	if h.witness != nil {
		if err := h.witness.Start(clusterID, raftPeers); err != nil {
			slog.Error("configure-failover: failed to start Patroni witness",
				"cluster", clusterID, "error", err)
			warnings = append(warnings, "witness start failed: "+err.Error())
		} else {
			witnessAddr = h.witness.Addr(clusterID)
			slog.Info("configure-failover: Patroni witness started",
				"cluster", clusterID, "witness_addr", witnessAddr)
		}
	}

	type taskRef struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	// Helper: create + dispatch a task, advance to sent on success.
	dispatch := func(nodeID uuid.UUID, taskType protocol.TaskType, params []byte, timeoutSecs int) (string, error) {
		tid := uuid.New()
		_ = h.taskResults.Create(ctx, nodeID, tid, string(taskType), params)
		if err := h.dispatcher.Dispatch(nodeID, protocol.TaskDispatchPayload{
			TaskID:      tid.String(),
			TaskType:    taskType,
			Params:      json.RawMessage(params),
			TimeoutSecs: timeoutSecs,
		}); err != nil {
			return "", err
		}
		_ = h.taskResults.SetSent(ctx, tid)
		return tid.String(), nil
	}

	// Optional database backup before any destructive operations.
	// Fire-and-forget: the operator can track progress via the task list.
	// Patroni config dispatch proceeds regardless so the operator isn't
	// blocked if the backup takes a long time.
	var backupTask *taskRef
	if req.SaveBackup {
		bp, _ := json.Marshal(protocol.DBBackupParams{DBName: "netbox", DBUser: superUser})
		tid, err := dispatch(primaryNode.ID, protocol.TaskDBBackup, bp, 600)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"backup dispatch failed (primary not connected?): %v — proceeding without backup", err))
		} else {
			backupTask = &taskRef{
				NodeID:   primaryNode.ID.String(),
				Hostname: primaryNode.Hostname,
				TaskID:   tid,
				Status:   "dispatched",
			}
		}
	}

	// Stop NetBox on all non-primary nodes before configuring Patroni.
	// This prevents split-brain while Patroni is being reconfigured. The primary
	// keeps running so operators don't see an outage during the config push.
	// If AppTierAlwaysAvailable is enabled, NetBox will be restarted on non-primary
	// nodes after db_host is updated (Patroni primary election handles routing).
	stopTasks := make([]taskRef, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == primaryNode.ID {
			continue
		}
		stopParams, _ := json.Marshal(struct{}{})
		if tid, err := dispatch(node.ID, protocol.TaskStopNetbox, stopParams, 30); err != nil {
			warnings = append(warnings, fmt.Sprintf("stop-netbox dispatch failed for %s: %v", node.Hostname, err))
		} else {
			stopTasks = append(stopTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				TaskID: tid, Status: "dispatched",
			})
		}
	}

	// Push patroni.yml to every node, then restart Patroni.
	// Tasks are dispatched in order: install → write_config → restart.
	// The agent serializes tasks in a queue, so each step completes before the
	// next begins — install finishes before write_config, write_config before restart.
	patroniTasks := make([]taskRef, 0, len(nodes)*3)
	for _, node := range nodes {
		nodeIP := stripCIDR(node.IPAddress)

		// Step 1: Install Patroni (idempotent — apt/yum no-ops if already installed).
		// Uses a generous 5-minute timeout; package installs can be slow.
		installParams, _ := json.Marshal(protocol.PatroniInstallParams{})
		if tid, err := dispatch(node.ID, protocol.TaskInstallPatroni, installParams, 300); err != nil {
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "offline", Error: "install dispatch: " + err.Error(),
			})
			continue // skip write_config and restart if node is unreachable
		} else {
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				TaskID: tid, Status: "dispatched",
			})
		}

		// Per-node partner list excludes self
		partners := make([]string, 0, len(raftPeers)-1)
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
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "error", Error: "render: " + err.Error(),
			})
			continue
		}

		cfgParams, _ := json.Marshal(protocol.PatroniConfigWriteParams{Content: content})
		if tid, err := dispatch(node.ID, protocol.TaskWritePatroniConf, cfgParams, 30); err != nil {
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "offline", Error: err.Error(),
			})
		} else {
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				TaskID: tid, Status: "dispatched",
			})
		}

		restartParams, _ := json.Marshal(struct{}{})
		if tid, err := dispatch(node.ID, protocol.TaskRestartPatroni, restartParams, 60); err != nil {
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				Status: "offline", Error: "restart: " + err.Error(),
			})
		} else {
			patroniTasks = append(patroniTasks, taskRef{
				NodeID: node.ID.String(), Hostname: node.Hostname,
				TaskID: tid, Status: "dispatched",
			})
		}
	}

	// Push Sentinel config when app tier is always available.
	// Sentinel auth password (redis_password) is written into sentinel.conf
	// so all nodes and the Redis client library use the same secret.
	sentinelTasks := make([]taskRef, 0)
	if req.AppTierAlwaysAvailable {
		masterHost := stripCIDR(primaryNode.IPAddress)
		for _, node := range nodes {
			nodeIP := stripCIDR(node.IPAddress)
			content, sha256hex, err := configgen.RenderSentinel(configgen.SentinelInput{
				Scope:      req.RedisSentinelMaster,
				MasterHost: masterHost,
				BindAddr:   nodeIP,
				Password:   redisPassword,
			})
			if err != nil {
				sentinelTasks = append(sentinelTasks, taskRef{
					NodeID: node.ID.String(), Hostname: node.Hostname,
					Status: "error", Error: "render: " + err.Error(),
				})
				continue
			}
			sParams, _ := json.Marshal(protocol.SentinelConfigWriteParams{
				Content:      content,
				Sha256:       sha256hex,
				RestartAfter: true,
			})
			if tid, err := dispatch(node.ID, protocol.TaskWriteSentinelConf, sParams, 30); err != nil {
				sentinelTasks = append(sentinelTasks, taskRef{
					NodeID: node.ID.String(), Hostname: node.Hostname,
					Status: "offline", Error: err.Error(),
				})
			} else {
				sentinelTasks = append(sentinelTasks, taskRef{
					NodeID: node.ID.String(), Hostname: node.Hostname,
					TaskID: tid, Status: "dispatched",
				})
			}
		}
	}

	// For app_tier_always_available clusters, pre-set DATABASE.HOST on every
	// node to the identified primary's IP. This ensures all NetBox instances
	// connect to the correct primary before Patroni finishes electing a leader.
	// RestartAfter=false here — the Patroni restart tasks already trigger a
	// service restart cascade; NetBox will be started by the failover manager
	// or the operator as appropriate.
	if req.AppTierAlwaysAvailable {
		primaryIP := stripCIDR(primaryNode.IPAddress)
		dbParams, _ := json.Marshal(protocol.DBHostUpdateParams{
			Host:         primaryIP,
			RestartAfter: false,
		})
		for _, node := range nodes {
			tid, err := dispatch(node.ID, protocol.TaskUpdateDBHost, dbParams, 30)
			if err != nil {
				slog.Warn("configure-failover: db-host-update dispatch failed",
					"node", node.Hostname, "error", err)
				warnings = append(warnings, fmt.Sprintf(
					"db-host-update dispatch failed for %s: %v", node.Hostname, err))
			} else {
				slog.Info("configure-failover: db-host-update dispatched",
					"node", node.Hostname, "host", primaryIP, "task", tid)
			}
		}
	}

	// Restart NetBox on the primary after Patroni is configured so it picks up
	// any updated configuration. For non-primary nodes on app_tier_always_available
	// clusters, NetBox was stopped above; the db_host_update task already sets the
	// correct primary, and the operator (or auto-failover) will bring them back up.
	var netboxRestartTask *taskRef
	restartNetboxParams, _ := json.Marshal(struct{}{})
	if tid, err := dispatch(primaryNode.ID, protocol.TaskRestartNetbox, restartNetboxParams, 30); err != nil {
		warnings = append(warnings, fmt.Sprintf("restart-netbox dispatch failed for primary %s: %v", primaryNode.Hostname, err))
	} else {
		netboxRestartTask = &taskRef{
			NodeID: primaryNode.ID.String(), Hostname: primaryNode.Hostname,
			TaskID: tid, Status: "dispatched",
		}
	}

	if err := h.clusters.SetPatroniConfigured(ctx, clusterID); err != nil {
		slog.Warn("configure-failover: failed to mark cluster as configured",
			"cluster", clusterID, "error", err)
	}

	slog.Info("configure-failover: complete",
		"cluster", clusterID,
		"primary_node", primaryNode.Hostname,
		"witness_addr", witnessAddr,
		"patroni_nodes", len(patroniTasks),
		"sentinel_nodes", len(sentinelTasks),
		"warnings", len(warnings),
	)

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":           clusterID.String(),
		"witness_addr":         witnessAddr,
		"primary_node":         primaryNode.Hostname,
		"started_at":           startedAt.UTC().Format(time.RFC3339),
		"backup_task":          backupTask,
		"stop_tasks":           stopTasks,
		"patroni_tasks":        patroniTasks,
		"sentinel_tasks":       sentinelTasks,
		"netbox_restart_task":  netboxRestartTask,
		"warnings":             warnings,
	})
}

// ListFailoverEvents returns the most recent failover/failback events for a cluster.
// GET /api/v1/clusters/:id/failover-events
func (h *PatroniHandler) ListFailoverEvents(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	events, err := h.failoverEvents.ListByCluster(c.Request().Context(), clusterID, 50)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list failover events")
	}
	if events == nil {
		events = []queries.FailoverEvent{}
	}
	return c.JSON(http.StatusOK, events)
}
