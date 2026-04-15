package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/abottVU/netbox-failover/internal/server/crypto"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/abottVU/netbox-failover/internal/server/hub"
	"github.com/abottVU/netbox-failover/internal/server/sse"
	"github.com/abottVU/netbox-failover/internal/shared/protocol"
)

const serverVersion = "0.1.0"

// AgentHandler handles agent WebSocket connections and registration.
type AgentHandler struct {
	hub         *hub.Hub
	dispatcher  *hub.Dispatcher
	broker      *sse.Broker
	nodes       *queries.NodeQuerier
	agentToks   *queries.AgentTokenQuerier
	regToks     *queries.RegistrationTokenQuerier
	taskResults *queries.TaskResultQuerier
	enc         *crypto.Encryptor
}

func NewAgentHandler(
	h *hub.Hub,
	d *hub.Dispatcher,
	broker *sse.Broker,
	nodes *queries.NodeQuerier,
	agentToks *queries.AgentTokenQuerier,
	regToks *queries.RegistrationTokenQuerier,
	taskResults *queries.TaskResultQuerier,
	enc *crypto.Encryptor,
) *AgentHandler {
	return &AgentHandler{
		hub:         h,
		dispatcher:  d,
		broker:      broker,
		nodes:       nodes,
		agentToks:   agentToks,
		regToks:     regToks,
		taskResults: taskResults,
		enc:         enc,
	}
}

// ─── Registration ──────────────────────────────────────────────────────────────

type registerRequest struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token"` // one-time registration token
}

type registerResponse struct {
	NodeID      string `json:"node_id"`
	AgentToken  string `json:"agent_token"`
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
	nodeID, clusterID, ok := h.authenticate(ctx, hello)
	if !ok {
		if err := h.dispatcher.ServerHello(uuid.Nil, false, "authentication failed", serverVersion); err != nil {
			// Can't use dispatcher (session not registered), send directly
			rejectPayload, _ := json.Marshal(protocol.ServerHelloPayload{
				Accepted:     false,
				RejectReason: "authentication failed",
			})
			_ = wsjson.Write(ctx, conn, protocol.Envelope{
				ID:      uuid.New().String(),
				Type:    protocol.TypeServerHello,
				Payload: json.RawMessage(rejectPayload),
			})
		}
		conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return nil
	}

	// Step 3: Register session
	sess := hub.NewSession(nodeID, clusterID, conn)
	h.hub.Register(sess)
	defer func() {
		h.hub.Unregister(nodeID)
		_ = h.nodes.UpdateAgentStatus(context.Background(), nodeID, "disconnected")
		h.broker.Publish(sse.Event{
			Type:   sse.EventNodeStatus,
			NodeID: nodeID,
			Payload: map[string]any{
				"status":    "disconnected",
				"node_id":   nodeID,
			},
		})
		log.Printf("agent disconnected: node=%s", nodeID)
	}()

	// Update node status in DB
	_ = h.nodes.UpdateAgentStatus(ctx, nodeID, "connected")
	_ = h.agentToks.Touch(ctx, crypto.HashToken(hello.Token))

	// Step 4: Send server.hello
	welcomePayload, _ := json.Marshal(protocol.ServerHelloPayload{
		Accepted:      true,
		ServerVersion: serverVersion,
	})
	_ = wsjson.Write(ctx, conn, protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeServerHello,
		Payload: json.RawMessage(welcomePayload),
	})

	// Publish connected event
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
	log.Printf("agent connected: node=%s hostname=%s version=%s", nodeID, hello.Hostname, hello.AgentVersion)

	// Step 5: Start write pump and read pump concurrently
	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	go sess.WritePump(pumpCtx)
	h.readPump(pumpCtx, sess, conn)

	return nil
}

// authenticate validates the agent's NodeID + Token against the DB.
// Returns nodeID, clusterID, and whether auth succeeded.
func (h *AgentHandler) authenticate(ctx context.Context, hello protocol.AgentHelloPayload) (uuid.UUID, uuid.UUID, bool) {
	nodeID, err := uuid.Parse(hello.NodeID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}

	tokenHash := crypto.HashToken(hello.Token)
	tok, err := h.agentToks.GetValid(ctx, tokenHash)
	if err != nil || tok.NodeID != nodeID {
		return uuid.Nil, uuid.Nil, false
	}

	node, err := h.nodes.GetByID(ctx, nodeID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}

	return nodeID, node.ClusterID, true
}

// readPump reads inbound messages from the agent and dispatches them.
func (h *AgentHandler) readPump(ctx context.Context, sess *hub.Session, conn *websocket.Conn) {
	for {
		var env protocol.Envelope
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return // connection closed or context cancelled
		}
		sess.TouchLastSeen()
		h.handleInbound(ctx, sess, env)
	}
}

// handleInbound routes an inbound agent message to the appropriate handler.
func (h *AgentHandler) handleInbound(ctx context.Context, sess *hub.Session, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeAgentHeartbeat:
		h.handleHeartbeat(ctx, sess, env)
	case protocol.TypePatroniState:
		h.handlePatroniState(ctx, sess, env)
	case protocol.TypeTaskAck:
		h.handleTaskAck(ctx, sess, env)
	case protocol.TypeTaskResult:
		h.handleTaskResult(ctx, sess, env)
	default:
		log.Printf("unknown message type from node=%s: %s", sess.NodeID, env.Type)
	}
}

func (h *AgentHandler) handleHeartbeat(ctx context.Context, sess *hub.Session, env protocol.Envelope) {
	var hb protocol.HeartbeatPayload
	if err := json.Unmarshal(env.Payload, &hb); err != nil {
		return
	}

	var patroniStateJSON json.RawMessage
	if hb.PatroniState != nil {
		patroniStateJSON = *hb.PatroniState
	}

	if err := h.nodes.UpdateHeartbeat(ctx, sess.NodeID, hb.NetboxRunning, hb.RQRunning, patroniStateJSON); err != nil {
		log.Printf("heartbeat DB update failed node=%s: %v", sess.NodeID, err)
	}
	log.Printf("heartbeat node=%s load=%.2f mem=%.1f%% disk=%.1f%% netbox=%v rq=%v patroni=%s",
		sess.NodeID, hb.LoadAvg1, hb.MemUsedPct, hb.DiskUsedPct, hb.NetboxRunning, hb.RQRunning, hb.PatroniRole)

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

func (h *AgentHandler) handlePatroniState(ctx context.Context, sess *hub.Session, env protocol.Envelope) {
	var ps protocol.PatroniStatePayload
	if err := json.Unmarshal(env.Payload, &ps); err != nil {
		return
	}
	log.Printf("patroni role change node=%s: %s -> %s", sess.NodeID, ps.PrevRole, ps.Role)
	h.broker.Publish(sse.Event{
		Type:   sse.EventPatroniState,
		NodeID: sess.NodeID,
		Payload: map[string]any{
			"node_id":   sess.NodeID,
			"role":      ps.Role,
			"prev_role": ps.PrevRole,
		},
	})
}

func (h *AgentHandler) handleTaskAck(ctx context.Context, sess *hub.Session, env protocol.Envelope) {
	var ack protocol.TaskAckPayload
	if err := json.Unmarshal(env.Payload, &ack); err != nil {
		return
	}
	log.Printf("task ack node=%s task=%s status=%s", sess.NodeID, ack.TaskID, ack.Status)
	if taskID, err := uuid.Parse(ack.TaskID); err == nil {
		_ = h.taskResults.SetAck(ctx, taskID)
	}
}

func (h *AgentHandler) handleTaskResult(ctx context.Context, sess *hub.Session, env protocol.Envelope) {
	var result protocol.TaskResultPayload
	if err := json.Unmarshal(env.Payload, &result); err != nil {
		return
	}
	log.Printf("task result node=%s task=%s success=%v duration=%dms",
		sess.NodeID, result.TaskID, result.Success, result.DurationMs)

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
