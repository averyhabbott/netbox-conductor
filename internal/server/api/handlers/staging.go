package handlers

import (
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// StagingHandler manages staging tokens and the unassigned-agents pool.
type StagingHandler struct {
	stagingToks   *queries.StagingTokenQuerier
	stagingAgents *queries.StagingAgentQuerier
	nodes         *queries.NodeQuerier
	agentToks     *queries.AgentTokenQuerier
	h             *hub.Hub
	broker        *sse.Broker
}

func NewStagingHandler(
	stagingToks *queries.StagingTokenQuerier,
	stagingAgents *queries.StagingAgentQuerier,
	nodes *queries.NodeQuerier,
	agentToks *queries.AgentTokenQuerier,
	h *hub.Hub,
	broker *sse.Broker,
) *StagingHandler {
	return &StagingHandler{
		stagingToks:   stagingToks,
		stagingAgents: stagingAgents,
		nodes:         nodes,
		agentToks:     agentToks,
		h:             h,
		broker:        broker,
	}
}

// ── Staging tokens ─────────────────────────────────────────────────────────────

type createStagingTokenRequest struct {
	Label     string `json:"label"`
	ExpiresIn int    `json:"expires_in_hours"` // default 24
}

type stagingTokenResponse struct {
	ID        string  `json:"id"`
	Token     string  `json:"token,omitempty"` // only on create
	Label     string  `json:"label"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt string  `json:"expires_at"`
	UsedAt    *string `json:"used_at"`
}

func toStagingTokenResponse(t queries.StagingToken, rawToken string) stagingTokenResponse {
	r := stagingTokenResponse{
		ID:        t.ID.String(),
		Token:     rawToken,
		Label:     t.Label,
		CreatedAt: t.CreatedAt.Format(time.RFC3339),
		ExpiresAt: t.ExpiresAt.Format(time.RFC3339),
	}
	if t.UsedAt != nil {
		s := t.UsedAt.Format(time.RFC3339)
		r.UsedAt = &s
	}
	return r
}

// ListStagingTokens returns all staging tokens (used and unused).
// GET /api/v1/staging/tokens
func (h *StagingHandler) ListStagingTokens(c echo.Context) error {
	tokens, err := h.stagingToks.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list staging tokens")
	}
	if tokens == nil {
		tokens = []queries.StagingToken{}
	}
	resp := make([]stagingTokenResponse, len(tokens))
	for i, t := range tokens {
		resp[i] = toStagingTokenResponse(t, "") // raw token not returned after creation
	}
	return c.JSON(http.StatusOK, resp)
}

// CreateStagingToken generates a new staging token.
// POST /api/v1/staging/tokens
func (h *StagingHandler) CreateStagingToken(c echo.Context) error {
	var req createStagingTokenRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}

	hours := req.ExpiresIn
	if hours <= 0 {
		hours = 24
	}
	expiresAt := time.Now().Add(time.Duration(hours) * time.Hour)

	rawToken, err := crypto.GenerateToken(48)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "token generation failed")
	}

	tok, err := h.stagingToks.Create(c.Request().Context(), crypto.HashToken(rawToken), req.Label, expiresAt)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "token storage failed")
	}

	return c.JSON(http.StatusCreated, toStagingTokenResponse(*tok, rawToken))
}

// DeleteStagingToken removes a staging token.
// DELETE /api/v1/staging/tokens/:id
func (h *StagingHandler) DeleteStagingToken(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid token id")
	}
	if err := h.stagingToks.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete token")
	}
	return c.NoContent(http.StatusNoContent)
}

// ── Staging agents ─────────────────────────────────────────────────────────────

type stagingAgentResponse struct {
	ID           string  `json:"id"`
	Hostname     string  `json:"hostname"`
	IPAddress    string  `json:"ip_address"`
	OS           string  `json:"os"`
	Arch         string  `json:"arch"`
	AgentVersion string  `json:"agent_version"`
	Status       string  `json:"status"`
	Connected    bool    `json:"connected"`
	LastSeenAt   *string `json:"last_seen_at"`
	CreatedAt    string  `json:"created_at"`
}

func toStagingAgentResponse(a queries.StagingAgent, connected bool) stagingAgentResponse {
	r := stagingAgentResponse{
		ID:           a.ID.String(),
		Hostname:     a.Hostname,
		IPAddress:    a.IPAddress,
		OS:           a.OS,
		Arch:         a.Arch,
		AgentVersion: a.AgentVersion,
		Status:       a.Status,
		Connected:    connected,
		CreatedAt:    a.CreatedAt.Format(time.RFC3339),
	}
	if a.LastSeenAt != nil {
		s := a.LastSeenAt.Format(time.RFC3339)
		r.LastSeenAt = &s
	}
	return r
}

// ListStagingAgents returns all unassigned staging agents.
// GET /api/v1/staging/agents
func (h *StagingHandler) ListStagingAgents(c echo.Context) error {
	agents, err := h.stagingAgents.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list staging agents")
	}
	if agents == nil {
		agents = []queries.StagingAgent{}
	}
	resp := make([]stagingAgentResponse, len(agents))
	for i, a := range agents {
		resp[i] = toStagingAgentResponse(a, h.h.IsConnectedStaging(a.ID))
	}
	return c.JSON(http.StatusOK, resp)
}

// DeleteStagingAgent removes a staging agent (e.g. stale/rejected agent).
// DELETE /api/v1/staging/agents/:id
func (h *StagingHandler) DeleteStagingAgent(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid agent id")
	}
	// Disconnect if connected
	h.h.UnregisterStaging(id)
	if err := h.stagingAgents.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete staging agent")
	}
	return c.NoContent(http.StatusNoContent)
}

// AssignStagingAgent promotes a staging agent to a real node in the given cluster.
// POST /api/v1/staging/agents/:id/assign
func (h *StagingHandler) AssignStagingAgent(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid agent id")
	}

	var req struct {
		ClusterID        string `json:"cluster_id"`
		Role             string `json:"role"`
		FailoverPriority int    `json:"failover_priority"`
		SSHPort          int    `json:"ssh_port"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if req.ClusterID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "cluster_id is required")
	}
	clusterID, err := uuid.Parse(req.ClusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster_id")
	}
	if req.Role == "" {
		req.Role = "hyperconverged"
	}
	if req.SSHPort <= 0 {
		req.SSHPort = 22
	}
	if req.FailoverPriority <= 0 {
		req.FailoverPriority = 100
	}

	ctx := c.Request().Context()

	// Fetch the staging agent
	agent, err := h.stagingAgents.GetByID(ctx, id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "staging agent not found")
	}

	// Create the node record
	node, err := h.nodes.Create(ctx, queries.CreateNodeParams{
		ClusterID:        clusterID,
		Hostname:         agent.Hostname,
		IPAddress:        agent.IPAddress,
		Role:             req.Role,
		FailoverPriority: req.FailoverPriority,
		SSHPort:          req.SSHPort,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create node: "+err.Error())
	}

	// Migrate the staging agent's permanent token to agent_tokens
	if err := h.agentToks.Create(ctx, node.ID, agent.TokenHash); err != nil {
		// Roll back node creation on token migration failure
		_ = h.nodes.Delete(ctx, node.ID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to migrate agent token")
	}

	// Delete the staging agent row
	if err := h.stagingAgents.Delete(ctx, id); err != nil {
		// Non-fatal — the node is created and token is valid; log and continue
	}

	// Disconnect the staging session so the agent reconnects as a real node
	// (agent must be reconfigured with the new node_id)
	h.h.UnregisterStaging(id)

	h.broker.Publish(sse.Event{
		Type: sse.EventNodeStatus,
		Payload: map[string]any{
			"event":      "staging_agent_assigned",
			"staging_id": id.String(),
			"node_id":    node.ID.String(),
			"cluster_id": clusterID.String(),
			"hostname":   node.Hostname,
		},
	})

	return c.JSON(http.StatusOK, map[string]any{
		"node_id":    node.ID.String(),
		"cluster_id": clusterID.String(),
		"hostname":   node.Hostname,
		"role":       node.Role,
	})
}
