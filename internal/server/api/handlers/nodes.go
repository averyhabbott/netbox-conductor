package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/logging"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// NodeHandler handles node CRUD and registration token endpoints.
type NodeHandler struct {
	nodes       *queries.NodeQuerier
	clusters    *queries.ClusterQuerier
	regToks     *queries.RegistrationTokenQuerier
	agentToks   *queries.AgentTokenQuerier
	taskResults *queries.TaskResultQuerier
	hub         *hub.Hub
	broker      *sse.Broker
	serverURL   string // base URL shown to operators in ENV snippet
	logDir      string
	logName     string
}

func NewNodeHandler(
	nodes *queries.NodeQuerier,
	regToks *queries.RegistrationTokenQuerier,
	agentToks *queries.AgentTokenQuerier,
	taskResults *queries.TaskResultQuerier,
	clusters *queries.ClusterQuerier,
	h *hub.Hub,
	broker *sse.Broker,
	serverURL, logDir, logName string,
) *NodeHandler {
	return &NodeHandler{
		nodes:       nodes,
		clusters:    clusters,
		regToks:     regToks,
		agentToks:   agentToks,
		taskResults: taskResults,
		hub:         h,
		broker:      broker,
		serverURL:   serverURL,
		logDir:      logDir,
		logName:     logName,
	}
}

// ── Response types ────────────────────────────────────────────────────────────

type nodeResponse struct {
	ID                string  `json:"id"`
	ClusterID         string  `json:"cluster_id"`
	Hostname          string  `json:"hostname"`
	IPAddress         string  `json:"ip_address"`
	Role              string  `json:"role"`
	FailoverPriority  int     `json:"failover_priority"`
	AgentStatus       string  `json:"agent_status"`
	NetboxRunning     *bool   `json:"netbox_running"`
	RQRunning         *bool   `json:"rq_running"`
	SuppressAutoStart bool    `json:"suppress_auto_start"`
	MaintenanceMode   bool    `json:"maintenance_mode"`
	SSHPort           int     `json:"ssh_port"`
	LastSeenAt        *string `json:"last_seen_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toNodeResponse(n *queries.Node) nodeResponse {
	// PostgreSQL inet::text returns CIDR notation (e.g. "192.168.1.1/32"); strip prefix length.
	ip := n.IPAddress
	if i := strings.IndexByte(ip, '/'); i != -1 {
		ip = ip[:i]
	}
	r := nodeResponse{
		ID:                n.ID.String(),
		ClusterID:         n.ClusterID.String(),
		Hostname:          n.Hostname,
		IPAddress:         ip,
		Role:              n.Role,
		FailoverPriority:  n.FailoverPriority,
		AgentStatus:       n.AgentStatus,
		NetboxRunning:     n.NetboxRunning,
		MaintenanceMode:   n.MaintenanceMode,
		RQRunning:         n.RQRunning,
		SuppressAutoStart: n.SuppressAutoStart,
		SSHPort:           n.SSHPort,
		CreatedAt:         n.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         n.UpdatedAt.Format(time.RFC3339),
	}
	if n.LastSeenAt != nil {
		s := n.LastSeenAt.Format(time.RFC3339)
		r.LastSeenAt = &s
	}
	return r
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// List godoc
// GET /api/v1/clusters/:id/nodes
func (h *NodeHandler) List(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	nodes, err := h.nodes.ListByCluster(c.Request().Context(), clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	resp := make([]nodeResponse, 0, len(nodes))
	for i := range nodes {
		r := toNodeResponse(&nodes[i])
		// Override agent status with live hub state
		if h.hub.IsConnected(nodes[i].ID) {
			r.AgentStatus = "connected"
		}
		resp = append(resp, r)
	}
	return c.JSON(http.StatusOK, resp)
}

type createNodeRequest struct {
	Hostname         string `json:"hostname"`
	IPAddress        string `json:"ip_address"`
	Role             string `json:"role"`
	FailoverPriority int    `json:"failover_priority"`
	SSHPort          int    `json:"ssh_port"`
}

// Create godoc
// POST /api/v1/clusters/:id/nodes
func (h *NodeHandler) Create(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req createNodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Hostname == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "hostname is required")
	}
	if req.IPAddress == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "ip_address is required")
	}
	if req.Role != "hyperconverged" && req.Role != "app" && req.Role != "db_only" {
		return echo.NewHTTPError(http.StatusBadRequest, "role must be hyperconverged, app, or db_only")
	}
	if req.FailoverPriority == 0 {
		req.FailoverPriority = 100
	}
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}

	node, err := h.nodes.Create(c.Request().Context(), queries.CreateNodeParams{
		ClusterID:        clusterID,
		Hostname:         req.Hostname,
		IPAddress:        req.IPAddress,
		Role:             req.Role,
		FailoverPriority: req.FailoverPriority,
		SSHPort:          req.SSHPort,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusConflict, "hostname already exists in this cluster")
	}

	return c.JSON(http.StatusCreated, toNodeResponse(node))
}

// Get godoc
// GET /api/v1/clusters/:id/nodes/:nid
func (h *NodeHandler) Get(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	node, err := h.nodes.GetByID(c.Request().Context(), nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	r := toNodeResponse(node)
	if h.hub.IsConnected(nid) {
		r.AgentStatus = "connected"
	}
	return c.JSON(http.StatusOK, r)
}

type updateNodeRequest struct {
	FailoverPriority  *int  `json:"failover_priority"`
	SuppressAutoStart *bool `json:"suppress_auto_start"`
}

// Update godoc
// PUT /api/v1/clusters/:id/nodes/:nid
func (h *NodeHandler) Update(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	var req updateNodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	ctx := c.Request().Context()

	if req.FailoverPriority != nil {
		if err := h.nodes.UpdatePriority(ctx, nid, *req.FailoverPriority); err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
	}
	if req.SuppressAutoStart != nil {
		if err := h.nodes.SetSuppressAutoStart(ctx, nid, *req.SuppressAutoStart); err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
	}

	node, err := h.nodes.GetByID(ctx, nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	return c.JSON(http.StatusOK, toNodeResponse(node))
}

// Delete godoc
// DELETE /api/v1/clusters/:id/nodes/:nid
func (h *NodeHandler) Delete(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	// Revoke any active agent tokens so reconnect attempts fail auth.
	_ = h.agentToks.Revoke(c.Request().Context(), nid)

	// Force-disconnect the agent if it is currently connected.
	h.hub.Unregister(nid)

	if err := h.nodes.Delete(c.Request().Context(), nid); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	// Notify UI clients so the cluster page updates immediately.
	h.broker.Publish(sse.Event{
		Type:    sse.EventNodeStatus,
		NodeID:  nid,
		Payload: map[string]any{"event": "node_deleted"},
	})

	return c.NoContent(http.StatusNoContent)
}

// Status godoc
// GET /api/v1/clusters/:id/nodes/:nid/status
func (h *NodeHandler) Status(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	node, err := h.nodes.GetByID(c.Request().Context(), nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	r := toNodeResponse(node)
	if h.hub.IsConnected(nid) {
		r.AgentStatus = "connected"
	}
	return c.JSON(http.StatusOK, r)
}

// GenerateRegToken generates a one-time registration token for a node.
// POST /api/v1/clusters/:id/nodes/:nid/registration-token
func (h *NodeHandler) GenerateRegToken(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	// Verify node exists
	node, err := h.nodes.GetByID(c.Request().Context(), nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	// Generate raw token and store its hash
	rawToken, err := crypto.GenerateToken(48)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate token")
	}

	expiresAt := time.Now().Add(2 * time.Hour)
	if err := h.regToks.Create(
		c.Request().Context(),
		nid,
		crypto.HashToken(rawToken),
		expiresAt,
	); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store token")
	}

	// Build ENV snippet for operator to paste.
	// Always use wss:// in the snippet regardless of how serverURL was configured.
	wsURL := strings.NewReplacer("https://", "wss://", "http://", "ws://").Replace(h.serverURL)
	if wsURL == "" {
		wsURL = "wss://your-conductor"
	}
	envSnippet := fmt.Sprintf("AGENT_NODE_ID=%s\nAGENT_TOKEN=%s\nAGENT_SERVER_URL=%s/api/v1/agent/connect",
		node.ID.String(), rawToken, wsURL)

	return c.JSON(http.StatusOK, map[string]any{
		"token":       rawToken,
		"expires_at":  expiresAt.Format(time.RFC3339),
		"env_snippet": envSnippet,
		"node_id":     node.ID.String(),
		"hostname":    node.Hostname,
	})
}

// ServiceAction dispatches a start/stop/restart task for NetBox on a node.
// POST /api/v1/clusters/:id/nodes/:nid/{start,stop,restart}-netbox
func (h *NodeHandler) ServiceAction(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	// Determine task type from the route path segment
	var taskType protocol.TaskType
	switch {
	case strings.HasSuffix(c.Path(), "/start-netbox"):
		taskType = protocol.TaskStartNetbox
	case strings.HasSuffix(c.Path(), "/stop-netbox"):
		taskType = protocol.TaskStopNetbox
	case strings.HasSuffix(c.Path(), "/restart-netbox"):
		taskType = protocol.TaskRestartNetbox
	case strings.HasSuffix(c.Path(), "/restart-rq"):
		taskType = protocol.TaskRestartRQ
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown service action")
	}

	taskID := uuid.New()
	payload, _ := json.Marshal(protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    taskType,
		Params:      json.RawMessage(`{}`),
		TimeoutSecs: 30,
	})

	sess := h.hub.Get(nid)
	if sess == nil {
		return echo.NewHTTPError(http.StatusConflict, "node is not connected")
	}

	// Record the task before dispatching so it appears in history and can time out.
	_ = h.taskResults.Create(c.Request().Context(), nid, taskID, string(taskType), payload)

	if !sess.Send(protocol.Envelope{
		ID:      taskID.String(),
		Type:    protocol.TypeTaskDispatch,
		Payload: json.RawMessage(payload),
	}) {
		return echo.NewHTTPError(http.StatusConflict, "node send buffer full")
	}
	_ = h.taskResults.SetSent(c.Request().Context(), taskID)

	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":   taskID.String(),
		"task_type": taskType,
		"node_id":   nid.String(),
		"status":    "dispatched",
	})
}

// Tasks returns recent task history for a node.
// GET /api/v1/clusters/:id/nodes/:nid/tasks
func (h *NodeHandler) Tasks(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}
	limit := 20
	if l := c.QueryParam("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	tasks, err := h.taskResults.ListByNode(c.Request().Context(), nid, limit)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list tasks")
	}

	type taskRow struct {
		TaskID      string  `json:"task_id"`
		TaskType    string  `json:"task_type"`
		Status      string  `json:"status"`
		QueuedAt    string  `json:"queued_at"`
		CompletedAt *string `json:"completed_at,omitempty"`
	}

	rows := make([]taskRow, 0, len(tasks))
	for _, t := range tasks {
		row := taskRow{
			TaskID:   t.TaskID.String(),
			TaskType: t.TaskType,
			Status:   t.Status,
			QueuedAt: t.QueuedAt.Format(time.RFC3339),
		}
		if t.CompletedAt != nil {
			s := t.CompletedAt.Format(time.RFC3339)
			row.CompletedAt = &s
		}
		rows = append(rows, row)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"node_id": nid.String(),
		"tasks":   rows,
		"limit":   limit,
	})
}

// SetMaintenance toggles maintenance mode for a node.
// PUT /api/v1/clusters/:id/nodes/:nid/maintenance
// Body: {"enabled": true|false}
func (h *NodeHandler) SetMaintenance(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	if err := h.nodes.SetMaintenanceMode(c.Request().Context(), nid, body.Enabled); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	node, err := h.nodes.GetByID(c.Request().Context(), nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch node")
	}
	return c.JSON(http.StatusOK, toNodeResponse(node))
}

// GetLogs returns the last N lines of the per-agent log file for a node.
// GET /api/v1/clusters/:id/nodes/:nid/logs?lines=200
func (h *NodeHandler) GetLogs(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	lines := 200
	if s := c.QueryParam("lines"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			lines = n
		}
	}

	ctx := c.Request().Context()

	node, err := h.nodes.GetByID(ctx, nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	path := logging.AgentLogPath(h.logDir, h.logName, cluster.Name, node.Hostname)
	if c.QueryParam("source") == "netbox" {
		logName := c.QueryParam("log_name")
		if logName == "" {
			logName = "netbox.log"
		}
		path = logging.NetboxLogPath(h.logDir, h.logName, cluster.Name, node.Hostname, logName)
	}
	logLines, err := logging.TailFile(path, lines)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read log file")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"lines": logLines,
		"path":  path,
		"node":  node.Hostname,
	})
}

// DownloadAgentEnv generates a one-time registration token and returns a
// pre-populated netbox-agent.env file ready to install on the agent node.
// POST /api/v1/clusters/:id/nodes/:nid/agent-env
func (h *NodeHandler) DownloadAgentEnv(c echo.Context) error {
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	node, err := h.nodes.GetByID(c.Request().Context(), nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	rawToken, err := crypto.GenerateToken(48)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate token")
	}

	expiresAt := time.Now().Add(2 * time.Hour)
	if err := h.regToks.Create(c.Request().Context(), nid, crypto.HashToken(rawToken), expiresAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store token")
	}

	// Build the WebSocket URL from serverURL
	conductorHTTPS := h.serverURL
	if conductorHTTPS == "" {
		conductorHTTPS = "https://your-conductor-server"
	}
	wsURL := strings.NewReplacer("https://", "wss://", "http://", "ws://").Replace(conductorHTTPS)

	content := fmt.Sprintf(`# NetBox Agent configuration for %s
# Generated by NetBox Conductor on %s
# Token expires: %s
#
# Install steps:
#   1. Copy this file to /etc/netbox-agent/netbox-agent.env  (root:netbox-agent 0640)
#   2. Download the conductor CA cert:
#        curl -k -o /etc/netbox-agent/ca.crt %s/api/v1/downloads/ca.crt
#   3. Download the agent binary:
#        curl -k -o /usr/local/bin/netbox-conductor-agent %s/api/v1/downloads/agent-linux-amd64
#        chmod +x /usr/local/bin/netbox-conductor-agent
#   4. Enable and start the service, then watch the conductor UI for the node to connect.

# ── Identity ──────────────────────────────────────────────────────────────────

AGENT_NODE_ID=%s
AGENT_TOKEN=%s

# ── Server connection ─────────────────────────────────────────────────────────

AGENT_SERVER_URL=%s/api/v1/agent/connect
AGENT_TLS_CA_CERT=/etc/netbox-agent/ca.crt
AGENT_TLS_SKIP_VERIFY=false
AGENT_RECONNECT_INTERVAL_SECS=10

# ── Logging ───────────────────────────────────────────────────────────────────

AGENT_LOG_LEVEL=info

# ── NetBox paths ──────────────────────────────────────────────────────────────

NETBOX_CONFIG_PATH=/opt/netbox/netbox/netbox/configuration.py
NETBOX_MEDIA_ROOT=/opt/netbox/netbox/media

# ── Patroni ───────────────────────────────────────────────────────────────────

PATRONI_REST_URL=http://127.0.0.1:8008
`,
		node.Hostname,
		time.Now().UTC().Format(time.RFC3339),
		expiresAt.UTC().Format(time.RFC3339),
		conductorHTTPS,
		conductorHTTPS,
		node.ID.String(),
		rawToken,
		wsURL,
	)

	filename := "netbox-agent-" + node.Hostname + ".env"
	c.Response().Header().Set("Content-Disposition", "attachment; filename="+filename)
	c.Response().Header().Set("Content-Type", "text/plain; charset=utf-8")
	return c.String(http.StatusOK, content)
}

// GetNetboxLogNames returns the list of NetBox log names that have been forwarded
// and stored for this node (e.g. "netbox.log", "django.log").
// GET /api/v1/clusters/:id/nodes/:nid/netbox-log-names
func (h *NodeHandler) GetNetboxLogNames(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	nid, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	ctx := c.Request().Context()

	node, err := h.nodes.GetByID(ctx, nid)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	names, err := logging.ListNetboxLogNames(h.logDir, h.logName, cluster.Name, node.Hostname)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list log names")
	}

	return c.JSON(http.StatusOK, map[string]any{"names": names})
}
