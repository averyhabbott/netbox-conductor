package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/averyhabbott/netbox-conductor/internal/server/alerting"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/failover"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/logging"
	"github.com/averyhabbott/netbox-conductor/internal/server/media"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const serverVersion = "0.1.0"

// AgentHandler handles agent WebSocket connections and registration.
type AgentHandler struct {
	hub           *hub.Hub
	dispatcher    *hub.Dispatcher
	broker        *sse.Broker
	nodes         *queries.NodeQuerier
	clusters      *queries.ClusterQuerier
	agentToks     *queries.AgentTokenQuerier
	regToks       *queries.RegistrationTokenQuerier
	stagingToks   *queries.StagingTokenQuerier
	stagingAgents *queries.StagingAgentQuerier
	taskResults   *queries.TaskResultQuerier
	enc           *crypto.Encryptor
	media         *media.Manager
	failover      *failover.Manager
	nodeLogQ      *queries.NodeLogQuerier
	alertSender   *alerting.Sender
	logDir        string
	logName       string
}

func NewAgentHandler(
	h *hub.Hub,
	d *hub.Dispatcher,
	broker *sse.Broker,
	nodes *queries.NodeQuerier,
	agentToks *queries.AgentTokenQuerier,
	regToks *queries.RegistrationTokenQuerier,
	stagingToks *queries.StagingTokenQuerier,
	stagingAgents *queries.StagingAgentQuerier,
	taskResults *queries.TaskResultQuerier,
	clusters *queries.ClusterQuerier,
	enc *crypto.Encryptor,
	fo *failover.Manager,
	logDir, logName string,
) *AgentHandler {
	return &AgentHandler{
		hub:           h,
		dispatcher:    d,
		broker:        broker,
		nodes:         nodes,
		clusters:      clusters,
		agentToks:     agentToks,
		regToks:       regToks,
		stagingToks:   stagingToks,
		stagingAgents: stagingAgents,
		taskResults:   taskResults,
		enc:           enc,
		media:         media.NewManager(),
		failover:      fo,
		logDir:        logDir,
		logName:       logName,
	}
}

// SetNodeLogQuerier wires up the node log querier (called from main after construction).
func (h *AgentHandler) SetNodeLogQuerier(q *queries.NodeLogQuerier) { h.nodeLogQ = q }

// SetAlertSender wires up the alert sender (called from main after construction).
func (h *AgentHandler) SetAlertSender(s *alerting.Sender) { h.alertSender = s }

// ─── Registration ──────────────────────────────────────────────────────────────

type registerRequest struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token"` // one-time registration token
}

type registerResponse struct {
	NodeID     string `json:"node_id"`
	AgentToken string `json:"agent_token"`
}

// stagingRegisterRequest is sent by agents that have no node assignment yet.
type stagingRegisterRequest struct {
	StagingToken string `json:"staging_token"`
	Hostname     string `json:"hostname"`
	IPAddress    string `json:"ip_address"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
}

type stagingRegisterResponse struct {
	StagingAgentID string `json:"staging_agent_id"`
	AgentToken     string `json:"agent_token"`
}

// Register exchanges a one-time registration token for a permanent agent token.
// POST /api/v1/agent/register
func (h *AgentHandler) Register(c echo.Context) error {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}

	nodeID, err := uuid.Parse(req.NodeID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node_id")
	}

	// Consume the registration token (marks it used, rejects if expired/already used)
	regTok, err := h.regToks.Consume(c.Request().Context(), crypto.HashToken(req.Token))
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired registration token")
	}
	if regTok.NodeID != nodeID {
		return echo.NewHTTPError(http.StatusUnauthorized, "token/node mismatch")
	}

	// Generate permanent agent token
	rawToken, err := crypto.GenerateToken(48)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "token generation failed")
	}
	if err := h.agentToks.Create(c.Request().Context(), nodeID, crypto.HashToken(rawToken)); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "token storage failed")
	}

	return c.JSON(http.StatusOK, registerResponse{
		NodeID:     nodeID.String(),
		AgentToken: rawToken,
	})
}

// StagingRegister exchanges a staging token for a permanent agent token.
// The agent is placed in the staging pool until an operator assigns it to a cluster.
// POST /api/v1/agent/staging-register
func (h *AgentHandler) StagingRegister(c echo.Context) error {
	var req stagingRegisterRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if req.StagingToken == "" || req.Hostname == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "staging_token and hostname are required")
	}

	ctx := c.Request().Context()

	// Consume the staging token
	if _, err := h.stagingToks.Consume(ctx, crypto.HashToken(req.StagingToken)); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired staging token")
	}

	// Generate permanent agent token
	rawToken, err := crypto.GenerateToken(48)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "token generation failed")
	}

	// Record the staging agent
	agent, err := h.stagingAgents.Create(ctx, queries.CreateStagingAgentParams{
		Hostname:     req.Hostname,
		IPAddress:    req.IPAddress,
		OS:           req.OS,
		Arch:         req.Arch,
		AgentVersion: req.AgentVersion,
		TokenHash:    crypto.HashToken(rawToken),
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "agent registration failed")
	}

	h.broker.Publish(sse.Event{
		Type: sse.EventNodeStatus,
		Payload: map[string]any{
			"event":            "staging_agent_registered",
			"staging_agent_id": agent.ID.String(),
			"hostname":         agent.Hostname,
		},
	})

	return c.JSON(http.StatusOK, stagingRegisterResponse{
		StagingAgentID: agent.ID.String(),
		AgentToken:     rawToken,
	})
}

// ─── WebSocket Connect ─────────────────────────────────────────────────────────

// Connect upgrades an HTTP connection to WebSocket and runs the agent session.
// GET /api/v1/agent/connect
func (h *AgentHandler) Connect(c echo.Context) error {
	conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{
		// Allow any origin for the agent (it's not a browser)
		InsecureSkipVerify: true,
	})
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	// Step 1: Read agent.hello (with a short auth deadline)
	authCtx, authCancel := context.WithTimeout(ctx, 15*time.Second)
	defer authCancel()

	var env protocol.Envelope
	if err := wsjson.Read(authCtx, conn, &env); err != nil {
		conn.Close(websocket.StatusPolicyViolation, "expected agent.hello")
		return nil
	}
	if env.Type != protocol.TypeAgentHello {
		conn.Close(websocket.StatusPolicyViolation, "expected agent.hello")
		return nil
	}

	var hello protocol.AgentHelloPayload
	if err := json.Unmarshal(env.Payload, &hello); err != nil {
		conn.Close(websocket.StatusPolicyViolation, "malformed agent.hello")
		return nil
	}

	// Step 2: Authenticate
	auth, ok := h.authenticate(ctx, hello)
	if !ok {
		rejectPayload, _ := json.Marshal(protocol.ServerHelloPayload{
			Accepted:     false,
			RejectReason: "authentication failed",
		})
		_ = wsjson.Write(ctx, conn, protocol.Envelope{
			ID:      uuid.New().String(),
			Type:    protocol.TypeServerHello,
			Payload: json.RawMessage(rejectPayload),
		})
		conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return nil
	}

	if auth.stagingAgent != nil {
		return h.connectStagingAgent(ctx, conn, hello, auth.stagingAgent)
	}
	return h.connectNode(ctx, conn, hello, auth.node)
}

// connectNode handles the WebSocket session for an assigned node agent.
func (h *AgentHandler) connectNode(ctx context.Context, conn *websocket.Conn, hello protocol.AgentHelloPayload, node *queries.Node) error {
	nodeID := node.ID
	clusterID := node.ClusterID

	// Look up cluster for logging and ServerHello config delivery.
	var cluster *queries.Cluster
	clusterName := clusterID.String()
	if c, err := h.clusters.GetByID(ctx, clusterID); err == nil {
		clusterName = c.Name
		cluster = c
	}

	agentLog := logging.OpenAgentLog(h.logDir, h.logName, clusterName, node.Hostname)

	sess := hub.NewSession(nodeID, clusterID, conn)
	sess.AgentVersion = hello.AgentVersion
	sess.Arch = hello.Arch
	h.hub.Register(sess)
	// Capture variables for the closure — don't close over the loop-local pointers.
	logDir, logName, cn, hn := h.logDir, h.logName, clusterName, node.Hostname
	sess.NetboxLogPathFn = func(logFilename string) string {
		return logging.NetboxLogPath(logDir, logName, cn, hn, logFilename)
	}
	defer func() {
		agentLog.Logger.Info("agent disconnected")
		agentLog.Close()
		h.hub.UnregisterIfSame(nodeID, sess)
		_ = h.nodes.UpdateAgentStatus(context.Background(), nodeID, "disconnected")
		h.broker.Publish(sse.Event{
			Type:   sse.EventNodeStatus,
			NodeID: nodeID,
			Payload: map[string]any{
				"status":  "disconnected",
				"node_id": nodeID,
			},
		})
		if h.failover != nil {
			h.failover.OnNodeDisconnect(nodeID, clusterID)
		}
		slog.Info("agent disconnected", "node", nodeID, "hostname", node.Hostname)

		// Persist a structured log entry so operators can see disconnect history.
		if h.nodeLogQ != nil {
			nid := nodeID
			_ = h.nodeLogQ.Insert(context.Background(), queries.InsertNodeLogParams{
				ClusterID: clusterID,
				NodeID:    &nid,
				Hostname:  node.Hostname,
				Level:     "warn",
				Source:    "conductor",
				Message:   "Agent disconnected",
			})
		}

		// Fire alert unless the node is in maintenance mode (suppress expected downtime).
		if h.alertSender != nil && !node.MaintenanceMode {
			go h.alertSender.FireAgentDisconnect(nodeID, clusterID, node.Hostname)
		}
	}()

	_ = h.nodes.UpdateAgentStatus(ctx, nodeID, "connected")
	_ = h.agentToks.Touch(ctx, crypto.HashToken(hello.Token))
	if h.failover != nil {
		h.failover.OnNodeConnect(nodeID, clusterID)
	}

	// Log the connect event and resolve any open disconnect alert.
	if h.nodeLogQ != nil {
		nid := nodeID
		_ = h.nodeLogQ.Insert(ctx, queries.InsertNodeLogParams{
			ClusterID: clusterID,
			NodeID:    &nid,
			Hostname:  node.Hostname,
			Level:     "info",
			Source:    "conductor",
			Message:   "Agent connected (v" + hello.AgentVersion + ")",
		})
	}
	if h.alertSender != nil {
		go h.alertSender.ResolveAgentDisconnect(nodeID, clusterID)
	}

	helloPayload := protocol.ServerHelloPayload{
		Accepted:      true,
		ServerVersion: serverVersion,
	}
	if cluster != nil {
		helloPayload.ClusterID              = clusterID.String()
		helloPayload.AppTierAlwaysAvailable = cluster.AppTierAlwaysAvailable
		helloPayload.PatroniScope           = cluster.PatroniScope
		helloPayload.PatroniConfigured      = cluster.PatroniConfigured
	}
	welcomePayload, _ := json.Marshal(helloPayload)
	_ = wsjson.Write(ctx, conn, protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeServerHello,
		Payload: json.RawMessage(welcomePayload),
	})

	h.broker.Publish(sse.Event{
		Type:   sse.EventNodeStatus,
		NodeID: nodeID,
		Payload: map[string]any{
			"status":        "connected",
			"node_id":       nodeID,
			"agent_version": hello.AgentVersion,
			"hostname":      hello.Hostname,
		},
	})
	agentLog.Logger.Info("agent connected",
		"agent_version", hello.AgentVersion,
		"log_path", agentLog.Path,
	)
	slog.Info("agent connected", "node", nodeID, "hostname", node.Hostname, "version", hello.AgentVersion)

	// Dispatch pending tasks on reconnect:
	//   "sent"   tasks were in-flight before the last disconnect — re-send them.
	//   "queued" tasks were created while the node was offline (e.g. a stop.netbox
	//            enqueued to prevent split-brain) — dispatch them for the first time.
	if pending, err := h.taskResults.ListPendingByNode(ctx, nodeID); err == nil && len(pending) > 0 {
		slog.Info("dispatching pending tasks on reconnect", "node", nodeID, "count", len(pending))
		for i := range pending {
			t := &pending[i]
			if len(t.RequestPayload) == 0 {
				continue
			}
			sess.Send(protocol.Envelope{
				ID:      t.TaskID.String(),
				Type:    protocol.TypeTaskDispatch,
				Payload: t.RequestPayload,
			})
			// Advance queued → sent; "sent" tasks are already in the correct state.
			if t.Status == "queued" {
				_ = h.taskResults.SetSent(ctx, t.TaskID)
			}
		}
	}

	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	go sess.PingLoop(pumpCtx)
	go sess.WritePump(pumpCtx)
	h.readPump(pumpCtx, sess, conn, agentLog)

	return nil
}

// connectStagingAgent handles the WebSocket session for an unassigned staging agent.
// Staging agents send heartbeats but cannot receive task dispatches.
func (h *AgentHandler) connectStagingAgent(ctx context.Context, conn *websocket.Conn, hello protocol.AgentHelloPayload, sa *queries.StagingAgent) error {
	agentLog := logging.OpenAgentLog(h.logDir, h.logName, "staging", sa.Hostname)

	sess := hub.NewSession(sa.ID, uuid.Nil, conn)
	h.hub.RegisterStaging(sa.ID, sess)
	defer func() {
		agentLog.Logger.Info("staging agent disconnected")
		agentLog.Close()
		h.hub.UnregisterStaging(sa.ID)
		_ = h.stagingAgents.UpdateStatus(context.Background(), sa.ID, "disconnected")
		h.broker.Publish(sse.Event{
			Type: sse.EventNodeStatus,
			Payload: map[string]any{
				"event":            "staging_agent_disconnected",
				"staging_agent_id": sa.ID.String(),
				"hostname":         sa.Hostname,
			},
		})
		slog.Info("staging agent disconnected", "staging_id", sa.ID, "hostname", sa.Hostname)
	}()

	_ = h.stagingAgents.UpdateStatus(ctx, sa.ID, "connected")

	welcomePayload, _ := json.Marshal(protocol.ServerHelloPayload{
		Accepted:      true,
		ServerVersion: serverVersion,
	})
	_ = wsjson.Write(ctx, conn, protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeServerHello,
		Payload: json.RawMessage(welcomePayload),
	})

	h.broker.Publish(sse.Event{
		Type: sse.EventNodeStatus,
		Payload: map[string]any{
			"event":            "staging_agent_connected",
			"staging_agent_id": sa.ID.String(),
			"hostname":         sa.Hostname,
			"agent_version":    hello.AgentVersion,
		},
	})
	slog.Info("staging agent connected", "staging_id", sa.ID, "hostname", sa.Hostname, "version", hello.AgentVersion)

	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	go sess.WritePump(pumpCtx)
	h.readPumpStaging(pumpCtx, sess, conn, agentLog)

	return nil
}

// authResult holds the outcome of authentication.
type authResult struct {
	node         *queries.Node         // set for assigned agents
	stagingAgent *queries.StagingAgent // set for staging agents
}

// authenticate validates the agent's NodeID + Token.
// It first tries normal node auth; if that fails it tries staging agent auth.
func (h *AgentHandler) authenticate(ctx context.Context, hello protocol.AgentHelloPayload) (*authResult, bool) {
	id, err := uuid.Parse(hello.NodeID)
	if err != nil {
		return nil, false
	}

	tokenHash := crypto.HashToken(hello.Token)

	// Try normal node auth first
	tok, err := h.agentToks.GetValid(ctx, tokenHash)
	if err == nil && tok.NodeID == id {
		node, err := h.nodes.GetByID(ctx, id)
		if err == nil {
			return &authResult{node: node}, true
		}
	}

	// Fall back to staging agent auth
	sa, err := h.stagingAgents.GetByTokenHash(ctx, tokenHash)
	if err == nil && sa.ID == id {
		return &authResult{stagingAgent: sa}, true
	}

	return nil, false
}

// readPump reads inbound messages from an assigned node agent and dispatches them.
func (h *AgentHandler) readPump(ctx context.Context, sess *hub.Session, conn *websocket.Conn, agentLog *logging.AgentLog) {
	for {
		var env protocol.Envelope
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return // connection closed or context cancelled
		}
		sess.TouchLastSeen()
		h.handleInbound(ctx, sess, env, agentLog.Logger)
	}
}

// readPumpStaging reads inbound messages from a staging (unassigned) agent.
// Only heartbeats are meaningful here; task messages are silently ignored.
func (h *AgentHandler) readPumpStaging(ctx context.Context, sess *hub.Session, conn *websocket.Conn, agentLog *logging.AgentLog) {
	for {
		var env protocol.Envelope
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return
		}
		sess.TouchLastSeen()
		// Staging agents can only send heartbeats (for last_seen tracking).
		// All other message types are ignored — no task dispatch, no patroni state.
		if env.Type == protocol.TypeAgentHeartbeat {
			_ = h.stagingAgents.UpdateStatus(ctx, sess.NodeID, "connected")
		}
	}
}

// handleInbound routes an inbound agent message to the appropriate handler.
func (h *AgentHandler) handleInbound(ctx context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	switch env.Type {
	case protocol.TypeAgentHeartbeat:
		h.handleHeartbeat(ctx, sess, env, logger)
	case protocol.TypePatroniState:
		h.handlePatroniState(ctx, sess, env, logger)
	case protocol.TypeTaskAck:
		h.handleTaskAck(ctx, sess, env, logger)
	case protocol.TypeTaskResult:
		h.handleTaskResult(ctx, sess, env, logger)
	case protocol.TypeMediaChunk:
		h.handleMediaChunk(ctx, sess, env, logger)
	case protocol.TypeMediaChunkAck:
		h.handleMediaChunkAck(ctx, sess, env, logger)
	case protocol.TypeNetboxLog:
		h.handleNetboxLog(ctx, sess, env, logger)
	default:
		logger.Warn("unknown message type", "type", env.Type)
	}
}

func (h *AgentHandler) handleHeartbeat(ctx context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	var hb protocol.HeartbeatPayload
	if err := json.Unmarshal(env.Payload, &hb); err != nil {
		return
	}

	var patroniStateJSON json.RawMessage
	if hb.PatroniState != nil {
		patroniStateJSON = *hb.PatroniState
	}

	if err := h.nodes.UpdateHeartbeat(ctx, sess.NodeID, hb.NetboxRunning, hb.RQRunning, patroniStateJSON); err != nil {
		logger.Warn("heartbeat DB update failed", "error", err)
	}

	// Detect netbox_running transitions and notify the failover manager.
	// prev == nil means this is the first heartbeat for this session — no transition to act on.
	netboxRunningPtr := &hb.NetboxRunning
	if prev, changed := sess.SetNetboxRunning(netboxRunningPtr); changed && prev != nil && h.failover != nil {
		wasRunning := *prev
		isRunning := hb.NetboxRunning
		if wasRunning && !isRunning {
			// NetBox stopped on a connected node — arm a failover grace timer.
			go h.failover.OnNetboxStopped(sess.NodeID, sess.ClusterID)
		} else if !wasRunning && isRunning {
			// NetBox restarted — cancel any pending failover timer.
			h.failover.OnNetboxStarted(sess.NodeID)
		}
	}

	if hb.NetboxVersion != "" {
		sess.NetboxVersion = hb.NetboxVersion
		if err := h.clusters.UpdateNetboxVersion(ctx, sess.ClusterID, hb.NetboxVersion); err != nil {
			logger.Warn("updating cluster netbox_version failed", "error", err)
		}
	}

	// Heartbeats are Debug — they fire every 15s and would flood Info logs.
	logger.Debug("heartbeat",
		"load", hb.LoadAvg1,
		"mem_pct", hb.MemUsedPct,
		"disk_pct", hb.DiskUsedPct,
		"netbox", hb.NetboxRunning,
		"rq", hb.RQRunning,
		"patroni_role", hb.PatroniRole,
	)

	h.broker.Publish(sse.Event{
		Type:   sse.EventNodeHeartbeat,
		NodeID: sess.NodeID,
		Payload: map[string]any{
			"node_id":        sess.NodeID,
			"load_avg_1":     hb.LoadAvg1,
			"load_avg_5":     hb.LoadAvg5,
			"mem_used_pct":   hb.MemUsedPct,
			"disk_used_pct":  hb.DiskUsedPct,
			"netbox_running": hb.NetboxRunning,
			"rq_running":     hb.RQRunning,
			"patroni_role":   hb.PatroniRole,
		},
	})
}

func (h *AgentHandler) handlePatroniState(ctx context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	var ps protocol.PatroniStatePayload
	if err := json.Unmarshal(env.Payload, &ps); err != nil {
		return
	}
	logger.Info("patroni role change", "prev_role", ps.PrevRole, "role", ps.Role)
	slog.Info("patroni role change", "node", sess.NodeID, "prev_role", ps.PrevRole, "role", ps.Role)
	h.broker.Publish(sse.Event{
		Type:   sse.EventPatroniState,
		NodeID: sess.NodeID,
		Payload: map[string]any{
			"node_id":   sess.NodeID,
			"role":      ps.Role,
			"prev_role": ps.PrevRole,
		},
	})

	// When a node becomes the Patroni primary on an app_tier_always_available
	// cluster, push the new primary's IP as DATABASE.HOST to all cluster nodes
	// so every NetBox instance reconnects to the writable database immediately.
	if ps.Role == "primary" {
		go h.dispatchDBHostUpdate(context.Background(), sess.NodeID, sess.ClusterID)
	}
}

// dispatchDBHostUpdate sends TaskUpdateDBHost to all connected nodes in the
// cluster. Only runs for app_tier_always_available clusters with Patroni
// configured; silently no-ops otherwise.
func (h *AgentHandler) dispatchDBHostUpdate(ctx context.Context, newPrimaryNodeID, clusterID uuid.UUID) {
	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil || !cluster.AppTierAlwaysAvailable || !cluster.PatroniConfigured {
		return
	}

	newPrimary, err := h.nodes.GetByID(ctx, newPrimaryNodeID)
	if err != nil {
		slog.Warn("db-host-update: could not fetch new primary node", "node", newPrimaryNodeID, "error", err)
		return
	}

	primaryIP := stripCIDR(newPrimary.IPAddress)
	slog.Info("db-host-update: dispatching DATABASE.HOST update to all cluster nodes",
		"cluster", clusterID, "new_primary", newPrimary.Hostname, "host", primaryIP)

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		slog.Warn("db-host-update: could not list cluster nodes", "cluster", clusterID, "error", err)
		return
	}

	params, _ := json.Marshal(protocol.DBHostUpdateParams{
		Host:         primaryIP,
		RestartAfter: true,
	})

	for _, node := range nodes {
		if !h.hub.IsConnected(node.ID) {
			slog.Debug("db-host-update: node not connected, skipping", "node", node.Hostname)
			continue
		}
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskUpdateDBHost), params)
		if err := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskUpdateDBHost,
			Params:      json.RawMessage(params),
			TimeoutSecs: 60,
		}); err != nil {
			slog.Warn("db-host-update: dispatch failed", "node", node.Hostname, "error", err)
		} else {
			_ = h.taskResults.SetSent(ctx, taskID)
			slog.Info("db-host-update: dispatched", "node", node.Hostname, "host", primaryIP)
		}
	}
}

func (h *AgentHandler) handleTaskAck(ctx context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	var ack protocol.TaskAckPayload
	if err := json.Unmarshal(env.Payload, &ack); err != nil {
		return
	}
	logger.Info("task ack", "task_id", ack.TaskID, "status", ack.Status)
	if taskID, err := uuid.Parse(ack.TaskID); err == nil {
		_ = h.taskResults.SetAck(ctx, taskID)
	}
}

func (h *AgentHandler) handleTaskResult(ctx context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	var result protocol.TaskResultPayload
	if err := json.Unmarshal(env.Payload, &result); err != nil {
		return
	}
	if result.Success {
		logger.Info("task result", "task_id", result.TaskID, "success", true, "duration_ms", result.DurationMs)
	} else {
		logger.Warn("task result: failed", "task_id", result.TaskID, "error", result.ErrorMsg, "duration_ms", result.DurationMs)
	}

	if taskID, err := uuid.Parse(result.TaskID); err == nil {
		responseJSON, _ := json.Marshal(map[string]any{
			"success":     result.Success,
			"output":      result.Output,
			"error":       result.ErrorMsg,
			"duration_ms": result.DurationMs,
		})
		_ = h.taskResults.Complete(ctx, taskID, result.Success, responseJSON)
	}

	h.broker.Publish(sse.Event{
		Type:   sse.EventTaskComplete,
		NodeID: sess.NodeID,
		Payload: map[string]any{
			"task_id":  result.TaskID,
			"success":  result.Success,
			"output":   result.Output,
			"error":    result.ErrorMsg,
			"duration": result.DurationMs,
		},
	})
}

// handleMediaChunk receives a chunk from the source agent and:
//  1. Writes it into the relay pipe so the relay goroutine forwards it to the target.
func (h *AgentHandler) handleMediaChunk(ctx context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	var chunk protocol.MediaChunkPayload
	if err := json.Unmarshal(env.Payload, &chunk); err != nil {
		logger.Warn("malformed media.chunk", "error", err)
		return
	}

	transferID, err := uuid.Parse(chunk.TransferID)
	if err != nil {
		logger.Warn("media.chunk: invalid transfer_id", "transfer_id", chunk.TransferID)
		return
	}

	t, ok := h.media.Get(transferID)
	if !ok {
		logger.Warn("media.chunk: unknown transfer", "transfer_id", chunk.TransferID)
		return
	}

	// Forward chunk envelope directly to target agent session.
	targetSess := h.hub.Get(t.TargetNode)
	if targetSess != nil {
		targetSess.Send(env) // forward as-is; target parses TypeMediaChunk
	}

	// Signal EOF into the pipe so the relay goroutine can clean up.
	if chunk.EOF && chunk.RelativePath == "" {
		h.media.Remove(transferID)
		logger.Info("media transfer complete", "transfer_id", chunk.TransferID)
	}
}

func (h *AgentHandler) handleNetboxLog(_ context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	if sess.NetboxLogPathFn == nil {
		return
	}
	var p protocol.NetboxLogPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	if len(p.Lines) == 0 {
		return
	}
	logName := p.LogName
	if logName == "" {
		logName = "netbox.log"
	}
	path := sess.NetboxLogPathFn(logName)
	if err := logging.AppendLines(path, p.Lines); err != nil {
		logger.Warn("failed to write netbox log lines", "log_name", logName, "error", err)
	}
}

// handleMediaChunkAck receives a backpressure ack from the target agent.
// Currently just logged; future: signal source agent to pace sends.
func (h *AgentHandler) handleMediaChunkAck(_ context.Context, sess *hub.Session, env protocol.Envelope, logger *slog.Logger) {
	var ack protocol.MediaChunkAckPayload
	if err := json.Unmarshal(env.Payload, &ack); err != nil {
		return
	}
	logger.Debug("media chunk ack", "transfer_id", ack.TransferID, "chunk", ack.ChunkIndex)
}

// StartMediaSync initiates a media sync from sourceNode → targetNode.
// POST /api/v1/clusters/:id/nodes/:nid/media-sync
func (h *AgentHandler) StartMediaSync(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	sourceNodeID, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}

	var req struct {
		TargetNodeID string `json:"target_node_id"`
		RelativePath string `json:"relative_path"`
		ChunkSize    int    `json:"chunk_size"`
	}
	if err := c.Bind(&req); err != nil || req.TargetNodeID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "target_node_id is required")
	}
	targetNodeID, err := uuid.Parse(req.TargetNodeID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid target_node_id")
	}

	ctx := c.Request().Context()

	// Verify both nodes belong to this cluster
	sourceNode, err := h.nodes.GetByID(ctx, sourceNodeID)
	if err != nil || sourceNode.ClusterID != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "source node not found in cluster")
	}
	targetNode, err := h.nodes.GetByID(ctx, targetNodeID)
	if err != nil || targetNode.ClusterID != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "target node not found in cluster")
	}

	if !h.hub.IsConnected(sourceNodeID) {
		return echo.NewHTTPError(http.StatusConflict, "source node is not connected")
	}
	if !h.hub.IsConnected(targetNodeID) {
		return echo.NewHTTPError(http.StatusConflict, "target node is not connected")
	}

	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 64 * 1024
	}

	transfer := h.media.Create(sourceNodeID, targetNodeID)

	taskID := uuid.New()
	params, _ := json.Marshal(protocol.MediaSyncParams{
		Direction:    "push_to_server",
		RelativePath: req.RelativePath,
		ChunkSizeB:   chunkSize,
		TransferID:   transfer.ID.String(),
	})

	_ = h.taskResults.Create(ctx, sourceNodeID, taskID, string(protocol.TaskMediaSync), params)

	if err := h.dispatcher.Dispatch(sourceNodeID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskMediaSync,
		Params:      json.RawMessage(params),
		TimeoutSecs: 3600, // media sync can take a while
	}); err != nil {
		h.media.Remove(transfer.ID)
		return echo.NewHTTPError(http.StatusConflict, "source node dispatch failed: "+err.Error())
	}

	_ = h.taskResults.SetSent(ctx, taskID)

	return c.JSON(http.StatusAccepted, map[string]any{
		"transfer_id":    transfer.ID.String(),
		"task_id":        taskID.String(),
		"source_node_id": sourceNodeID.String(),
		"target_node_id": targetNodeID.String(),
	})
}

// ClusterMediaSync initiates a cluster-level media sync.
// It auto-selects the most recently active connected app-tier or hyperconverged node
// as the source, and dispatches sync tasks to all other connected app-tier or
// hyperconverged nodes as targets.
// POST /api/v1/clusters/:id/media-sync
func (h *AgentHandler) ClusterMediaSync(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}
	if !cluster.MediaSyncEnabled {
		return echo.NewHTTPError(http.StatusConflict, "media sync is not enabled for this cluster")
	}

	allNodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	// Only app-tier and hyperconverged nodes participate in media sync.
	var appNodes []queries.Node
	for _, n := range allNodes {
		if n.Role == "app" || n.Role == "hyperconverged" {
			appNodes = append(appNodes, n)
		}
	}
	if len(appNodes) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "no app-tier or hyperconverged nodes in cluster")
	}

	// Source: most recently seen connected app node.
	var source *queries.Node
	for i := range appNodes {
		n := &appNodes[i]
		if !h.hub.IsConnected(n.ID) {
			continue
		}
		if source == nil || (n.LastSeenAt != nil && (source.LastSeenAt == nil || n.LastSeenAt.After(*source.LastSeenAt))) {
			source = n
		}
	}
	if source == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected app-tier or hyperconverged node available as sync source")
	}

	// Paths to sync: always includes MEDIA_ROOT (""), plus extra folders when enabled.
	syncPaths := []string{""}
	if cluster.ExtraFoldersSyncEnabled {
		for _, p := range cluster.ExtraSyncFolders {
			if p != "" {
				syncPaths = append(syncPaths, p)
			}
		}
	}

	type syncResult struct {
		TargetNodeID   string `json:"target_node_id"`
		TargetHostname string `json:"target_hostname"`
		TransferID     string `json:"transfer_id"`
		TaskID         string `json:"task_id"`
		SourcePath     string `json:"source_path,omitempty"`
	}
	var syncs []syncResult

	for i := range appNodes {
		target := &appNodes[i]
		if target.ID == source.ID || !h.hub.IsConnected(target.ID) {
			continue
		}
		for _, syncPath := range syncPaths {
			transfer := h.media.Create(source.ID, target.ID)
			taskID := uuid.New()
			params, _ := json.Marshal(protocol.MediaSyncParams{
				Direction:  "push_to_server",
				SourcePath: syncPath,
				ChunkSizeB: 64 * 1024,
				TransferID: transfer.ID.String(),
			})
			_ = h.taskResults.Create(ctx, source.ID, taskID, string(protocol.TaskMediaSync), params)
			if err := h.dispatcher.Dispatch(source.ID, protocol.TaskDispatchPayload{
				TaskID:      taskID.String(),
				TaskType:    protocol.TaskMediaSync,
				Params:      json.RawMessage(params),
				TimeoutSecs: 3600,
			}); err != nil {
				h.media.Remove(transfer.ID)
				continue
			}
			_ = h.taskResults.SetSent(ctx, taskID)
			syncs = append(syncs, syncResult{
				TargetNodeID:   target.ID.String(),
				TargetHostname: target.Hostname,
				TransferID:     transfer.ID.String(),
				TaskID:         taskID.String(),
				SourcePath:     syncPath,
			})
		}
	}

	if len(syncs) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "no eligible target nodes to sync to")
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"source_node_id":  source.ID.String(),
		"source_hostname": source.Hostname,
		"syncs":           syncs,
	})
}

// SyncConfig returns the cluster-level media sync configuration for this node.
// Used by the agent "check-sync-permissions" CLI command.
// GET /api/v1/agent/sync-config
func (h *AgentHandler) SyncConfig(c echo.Context) error {
	raw := c.Request().Header.Get("Authorization")
	rawToken, ok := strings.CutPrefix(raw, "Bearer ")
	if !ok || rawToken == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "bearer token required")
	}

	ctx := c.Request().Context()
	tok, err := h.agentToks.GetValid(ctx, crypto.HashToken(rawToken))
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or revoked token")
	}

	node, err := h.nodes.GetByID(ctx, tok.NodeID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}

	cluster, err := h.clusters.GetByID(ctx, node.ClusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	folders := cluster.ExtraSyncFolders
	if folders == nil {
		folders = []string{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"media_sync_enabled":          cluster.MediaSyncEnabled,
		"extra_folders_sync_enabled":  cluster.ExtraFoldersSyncEnabled,
		"extra_sync_folders":          folders,
	})
}
