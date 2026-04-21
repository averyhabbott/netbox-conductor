package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/api/middleware"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/nodestate"
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
	witnesses WitnessManager
	emitter   events.Emitter
}

func NewClusterHandler(
	clusters *queries.ClusterQuerier,
	nodes *queries.NodeQuerier,
	regToks *queries.RegistrationTokenQuerier,
	h *hub.Hub,
	witnesses *patroni.WitnessManager,
) *ClusterHandler {
	return &ClusterHandler{
		clusters:  clusters,
		nodes:     nodes,
		regToks:   regToks,
		hub:       h,
		witnesses: witnesses,
	}
}

func (h *ClusterHandler) SetEmitter(e events.Emitter) { h.emitter = e }

// actorFromCtx returns the requesting user's username, or "system" as a fallback.
func actorFromCtx(c echo.Context) string {
	if name, _ := c.Get(middleware.ContextKeyUsername).(string); name != "" {
		return name
	}
	return events.ActorSystem
}

// ── Response types ────────────────────────────────────────────────────────────

type clusterResponse struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	Mode                    string   `json:"mode"`
	AutoFailover            bool     `json:"auto_failover"`
	AutoFailback            bool     `json:"auto_failback"`
	AppTierAlwaysAvailable  bool     `json:"app_tier_always_available"`
	FailoverOnMaintenance   bool     `json:"failover_on_maintenance"`
	FailoverDelaySecs       int      `json:"failover_delay_secs"`
	FailbackMultiplier      int      `json:"failback_multiplier"`
	VIP                     *string  `json:"vip,omitempty"`
	PatroniScope            string   `json:"patroni_scope"`
	NetboxVersion           string   `json:"netbox_version"`
	MediaSyncEnabled        bool     `json:"media_sync_enabled"`
	ExtraFoldersSyncEnabled bool     `json:"extra_folders_sync_enabled"`
	ExtraSyncFolders        []string `json:"extra_sync_folders"`
	PatroniConfigured       bool     `json:"patroni_configured"`
	RedisSentinelMaster     string   `json:"redis_sentinel_master"`
	CreatedAt               string   `json:"created_at"`
	UpdatedAt               string   `json:"updated_at"`
}

func toClusterResponse(c *queries.Cluster) clusterResponse {
	folders := c.ExtraSyncFolders
	if folders == nil {
		folders = []string{}
	}
	return clusterResponse{
		ID:                      c.ID.String(),
		Name:                    c.Name,
		Description:             c.Description,
		Mode:                    c.Mode,
		AutoFailover:            c.AutoFailover,
		AutoFailback:            c.AutoFailback,
		AppTierAlwaysAvailable:  c.AppTierAlwaysAvailable,
		FailoverOnMaintenance:   c.FailoverOnMaintenance,
		FailoverDelaySecs:       c.FailoverDelaySecs,
		FailbackMultiplier:      c.FailbackMultiplier,
		VIP:                     c.VIP,
		PatroniScope:            c.PatroniScope,
		NetboxVersion:           c.NetboxVersion,
		MediaSyncEnabled:        c.MediaSyncEnabled,
		ExtraFoldersSyncEnabled: c.ExtraFoldersSyncEnabled,
		ExtraSyncFolders:        folders,
		PatroniConfigured:       c.PatroniConfigured,
		RedisSentinelMaster:     c.RedisSentinelMaster,
		CreatedAt:               c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:               c.UpdatedAt.Format(time.RFC3339),
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
		r := toClusterResponse(&clusters[i])
		if live := h.hub.NetboxVersionForCluster(clusters[i].ID); live != "" {
			r.NetboxVersion = live
		}
		resp = append(resp, r)
	}
	return c.JSON(http.StatusOK, resp)
}

type createClusterRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
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

	cluster, err := h.clusters.Create(c.Request().Context(), queries.CreateClusterParams{
		Name:          req.Name,
		Description:   req.Description,
		Mode:          req.Mode,
		PatroniScope:  req.PatroniScope,
		NetboxVersion: req.NetboxVersion,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusConflict, "cluster name already exists")
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryCluster, events.SeverityInfo, events.CodeClusterCreated,
			fmt.Sprintf("Cluster %q created", cluster.Name), actorFromCtx(c)).
			Cluster(cluster.ID).Build())
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

	r := toClusterResponse(cluster)
	if live := h.hub.NetboxVersionForCluster(id); live != "" {
		r.NetboxVersion = live
	}
	return c.JSON(http.StatusOK, r)
}

type updateFailoverRequest struct {
	AutoFailover           bool    `json:"auto_failover"`
	AutoFailback           bool    `json:"auto_failback"`
	AppTierAlwaysAvailable bool    `json:"app_tier_always_available"`
	FailoverOnMaintenance  bool    `json:"failover_on_maintenance"`
	FailoverDelaySecs      int     `json:"failover_delay_secs"`
	FailbackMultiplier     int     `json:"failback_multiplier"`
	VIP                    *string `json:"vip"`
	RedisSentinelMaster    string  `json:"redis_sentinel_master"`
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

	// Default delay to 30 s if caller omits or sends 0.
	if req.FailoverDelaySecs < 10 {
		req.FailoverDelaySecs = 10
	}
	if req.FailbackMultiplier <= 0 {
		req.FailbackMultiplier = 3
	}

	if err := h.clusters.UpdateFailoverSettings(c.Request().Context(), queries.UpdateClusterParams{
		ID:                     id,
		AutoFailover:           req.AutoFailover,
		AutoFailback:           req.AutoFailback,
		AppTierAlwaysAvailable: req.AppTierAlwaysAvailable,
		FailoverOnMaintenance:  req.FailoverOnMaintenance,
		FailoverDelaySecs:      req.FailoverDelaySecs,
		FailbackMultiplier:     req.FailbackMultiplier,
		VIP:                    req.VIP,
		RedisSentinelMaster:    req.RedisSentinelMaster,
	}); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	cluster, err := h.clusters.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch updated cluster")
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryCluster, events.SeverityInfo, events.CodeClusterFailoverUpdated,
			fmt.Sprintf("Failover settings updated for cluster %q", cluster.Name), actorFromCtx(c)).
			Cluster(id).Build())
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
	cluster, err := h.clusters.GetByID(c.Request().Context(), id)
	if err != nil {
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

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryCluster, events.SeverityWarn, events.CodeClusterDeleted,
			fmt.Sprintf("Cluster %q deleted", cluster.Name), actorFromCtx(c)).Build())
	}

	return c.NoContent(http.StatusNoContent)
}

type updateMediaSyncRequest struct {
	MediaSyncEnabled        bool     `json:"media_sync_enabled"`
	ExtraFoldersSyncEnabled bool     `json:"extra_folders_sync_enabled"`
	ExtraSyncFolders        []string `json:"extra_sync_folders"`
}

// UpdateMediaSyncSettings godoc
// PATCH /api/v1/clusters/:id/media-sync-settings
func (h *ClusterHandler) UpdateMediaSyncSettings(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req updateMediaSyncRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	if err := h.clusters.UpdateMediaSyncSettings(c.Request().Context(), queries.UpdateMediaSyncParams{
		ID:                      id,
		MediaSyncEnabled:        req.MediaSyncEnabled,
		ExtraFoldersSyncEnabled: req.ExtraFoldersSyncEnabled,
		ExtraSyncFolders:        req.ExtraSyncFolders,
	}); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	cluster, err := h.clusters.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch updated cluster")
	}

	return c.JSON(http.StatusOK, toClusterResponse(cluster))
}

// Status godoc
// GET /api/v1/clusters/:id/status
func (h *ClusterHandler) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	cluster, err := h.clusters.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	nodes, err := h.nodes.ListByCluster(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	connected := h.hub.ConnectedNodeIDs()
	connSet := make(map[string]bool, len(connected))
	for _, nid := range connected {
		connSet[nid.String()] = true
	}

	type nodeStatus struct {
		NodeID        string `json:"node_id"`
		Hostname      string `json:"hostname"`
		Role          string `json:"role"`
		AgentStatus   string `json:"agent_status"`
		NetboxRunning *bool  `json:"netbox_running"`
		RQRunning     *bool  `json:"rq_running"`
		PatroniRole   string `json:"patroni_role,omitempty"`
		Health        string `json:"health"`
		State         string `json:"state,omitempty"`
		LastSeenAt    string `json:"last_seen_at,omitempty"`
	}

	statuses := make([]nodeStatus, 0, len(nodes))
	healthSlice := make([]string, 0, len(nodes))
	stateSlice := make([]string, 0, len(nodes))
	roleSlice := make([]string, 0, len(nodes))

	for _, n := range nodes {
		// Live hub connection overrides the DB-stored agent_status.
		agentStatus := n.AgentStatus
		if connSet[n.ID.String()] {
			agentStatus = "connected"
		}

		patroniRole := nodestate.ExtractPatroniRole(n.PatroniState)
		health := nodestate.ComputeNodeHealth(n.Role, agentStatus, n.NetboxRunning, n.RQRunning, patroniRole, cluster.PatroniConfigured)
		state := nodestate.ComputeNodeState(n.Role, n.NetboxRunning, patroniRole, cluster.PatroniConfigured)

		ns := nodeStatus{
			NodeID:        n.ID.String(),
			Hostname:      n.Hostname,
			Role:          n.Role,
			AgentStatus:   agentStatus,
			NetboxRunning: n.NetboxRunning,
			RQRunning:     n.RQRunning,
			PatroniRole:   patroniRole,
			Health:        health,
			State:         state,
		}
		if n.LastSeenAt != nil {
			ns.LastSeenAt = n.LastSeenAt.Format(time.RFC3339)
		}
		statuses = append(statuses, ns)
		healthSlice = append(healthSlice, health)
		stateSlice = append(stateSlice, state)
		roleSlice = append(roleSlice, n.Role)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":     id.String(),
		"cluster_health": nodestate.AggregateClusterHealth(healthSlice, stateSlice, roleSlice),
		"nodes":          statuses,
	})
}
