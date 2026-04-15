package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

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
	clusters    *queries.ClusterQuerier
	nodes       *queries.NodeQuerier
	creds       *queries.CredentialQuerier
	taskResults *queries.TaskResultQuerier
	retention   *queries.RetentionQuerier
	enc         *crypto.Encryptor
	dispatcher  *hub.Dispatcher
	witness     *patroni.WitnessManager
}

func NewPatroniHandler(
	clusters *queries.ClusterQuerier,
	nodes *queries.NodeQuerier,
	creds *queries.CredentialQuerier,
	taskResults *queries.TaskResultQuerier,
	retention *queries.RetentionQuerier,
	enc *crypto.Encryptor,
	dispatcher *hub.Dispatcher,
	witness *patroni.WitnessManager,
) *PatroniHandler {
	return &PatroniHandler{
		clusters:    clusters,
		nodes:       nodes,
		creds:       creds,
		taskResults: taskResults,
		retention:   retention,
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
	_ = c.Bind(&req)

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
	_ = c.Bind(&req)

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
