package handlers

import (
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/patroni"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// WitnessManager is the subset of patroni.WitnessManager used by ClusterHandler.
type WitnessManager interface {
	Stop(clusterID uuid.UUID)
}

// ClusterHandler handles cluster CRUD endpoints.
type ClusterHandler struct {
	clusters  *queries.ClusterQuerier
	nodes     *queries.NodeQuerier
	regToks   *queries.RegistrationTokenQuerier
	hub       *hub.Hub
	enc       *crypto.Encryptor
	witnesses WitnessManager
}

func NewClusterHandler(
	clusters *queries.ClusterQuerier,
	nodes *queries.NodeQuerier,
	regToks *queries.RegistrationTokenQuerier,
	h *hub.Hub,
	enc *crypto.Encryptor,
	witnesses *patroni.WitnessManager,
) *ClusterHandler {
	return &ClusterHandler{
		clusters:  clusters,
		nodes:     nodes,
		regToks:   regToks,
		hub:       h,
		enc:       enc,
		witnesses: witnesses,
	}
}

// ── Response types ────────────────────────────────────────────────────────────

type clusterResponse struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Mode         string  `json:"mode"`
	AutoFailover bool    `json:"auto_failover"`
	AutoFailback bool    `json:"auto_failback"`
	VIP          *string `json:"vip,omitempty"`
	PatroniScope string  `json:"patroni_scope"`
	NetboxVersion string `json:"netbox_version"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

func toClusterResponse(c *queries.Cluster) clusterResponse {
	return clusterResponse{
		ID:           c.ID.String(),
		Name:         c.Name,
		Mode:         c.Mode,
		AutoFailover: c.AutoFailover,
		AutoFailback: c.AutoFailback,
		VIP:          c.VIP,
		PatroniScope: c.PatroniScope,
		NetboxVersion: c.NetboxVersion,
		CreatedAt:    c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    c.UpdatedAt.Format(time.RFC3339),
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// List godoc
// GET /api/v1/clusters
func (h *ClusterHandler) List(c echo.Context) error {
	clusters, err := h.clusters.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list clusters")
	}

	resp := make([]clusterResponse, 0, len(clusters))
	for i := range clusters {
		resp = append(resp, toClusterResponse(&clusters[i]))
	}
	return c.JSON(http.StatusOK, resp)
}

type createClusterRequest struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	PatroniScope  string `json:"patroni_scope"`
	NetboxVersion string `json:"netbox_version"`
}

// Create godoc
// POST /api/v1/clusters
func (h *ClusterHandler) Create(c echo.Context) error {
	var req createClusterRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if req.Mode != "active_standby" && req.Mode != "ha" {
		return echo.NewHTTPError(http.StatusBadRequest, "mode must be active_standby or ha")
	}
	if req.PatroniScope == "" {
		req.PatroniScope = req.Name
	}
	if req.NetboxVersion == "" {
		req.NetboxVersion = "4.x"
	}

	// Generate and encrypt secret key + pepper
	rawSecret, err := crypto.GenerateToken(50)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate secret key")
	}
	encSecret, err := h.enc.Encrypt([]byte(rawSecret))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt secret key")
	}

	rawPepper, err := crypto.GenerateToken(32)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate pepper")
	}
	encPepper, err := h.enc.Encrypt([]byte(rawPepper))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt pepper")
	}

	cluster, err := h.clusters.Create(c.Request().Context(), queries.CreateClusterParams{
		Name:            req.Name,
		Mode:            req.Mode,
		PatroniScope:    req.PatroniScope,
		NetboxVersion:   req.NetboxVersion,
		NetboxSecretKey: encSecret,
		APITokenPepper:  encPepper,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusConflict, "cluster name already exists")
	}

	return c.JSON(http.StatusCreated, toClusterResponse(cluster))
}

// Get godoc
// GET /api/v1/clusters/:id
func (h *ClusterHandler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	cluster, err := h.clusters.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	return c.JSON(http.StatusOK, toClusterResponse(cluster))
}

type updateFailoverRequest struct {
	AutoFailover bool    `json:"auto_failover"`
	AutoFailback bool    `json:"auto_failback"`
	VIP          *string `json:"vip"`
}

// UpdateFailoverSettings godoc
// PATCH /api/v1/clusters/:id/failover-settings
func (h *ClusterHandler) UpdateFailoverSettings(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req updateFailoverRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	if err := h.clusters.UpdateFailoverSettings(c.Request().Context(), queries.UpdateClusterParams{
		ID:           id,
		AutoFailover: req.AutoFailover,
		AutoFailback: req.AutoFailback,
		VIP:          req.VIP,
	}); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	cluster, err := h.clusters.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch updated cluster")
	}

	return c.JSON(http.StatusOK, toClusterResponse(cluster))
}

// Delete godoc
// DELETE /api/v1/clusters/:id
// Disconnects all connected agents, stops the Patroni witness, then deletes
// the cluster and all child records (cascaded in DB).
func (h *ClusterHandler) Delete(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	// Verify cluster exists before doing anything destructive
	if _, err := h.clusters.GetByID(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	// Disconnect all agents in this cluster
	h.hub.UnregisterCluster(id)

	// Stop the Patroni witness subprocess (no-op if none running)
	h.witnesses.Stop(id)

	// Delete from DB — CASCADE handles nodes, credentials, configs, tokens, tasks, audit logs
	if err := h.clusters.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete cluster")
	}

	return c.NoContent(http.StatusNoContent)
}

// Status godoc
// GET /api/v1/clusters/:id/status
func (h *ClusterHandler) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	nodes, err := h.nodes.ListByCluster(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	connected := h.hub.ConnectedNodeIDs()
	connSet := make(map[string]bool, len(connected))
	for _, nid := range connected {
		connSet[nid.String()] = true
	}

	type nodeStatus struct {
		NodeID        string `json:"node_id"`
		Hostname      string `json:"hostname"`
		AgentStatus   string `json:"agent_status"`
		NetboxRunning *bool  `json:"netbox_running"`
		RQRunning     *bool  `json:"rq_running"`
		PatroniRole   string `json:"patroni_role,omitempty"`
		LastSeenAt    string `json:"last_seen_at,omitempty"`
	}

	statuses := make([]nodeStatus, 0, len(nodes))
	for _, n := range nodes {
		// Live status overrides DB status
		status := n.AgentStatus
		if connSet[n.ID.String()] {
			status = "connected"
		}

		var patroniRole string
		if n.PatroniState != nil {
			// Extract role from JSONB — simplified; Phase 4 will parse fully
			patroniRole = ""
		}

		ns := nodeStatus{
			NodeID:        n.ID.String(),
			Hostname:      n.Hostname,
			AgentStatus:   status,
			NetboxRunning: n.NetboxRunning,
			RQRunning:     n.RQRunning,
			PatroniRole:   patroniRole,
		}
		if n.LastSeenAt != nil {
			ns.LastSeenAt = n.LastSeenAt.Format(time.RFC3339)
		}
		statuses = append(statuses, ns)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": id.String(),
		"nodes":      statuses,
	})
}
