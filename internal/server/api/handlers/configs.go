package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abottVU/netbox-failover/internal/server/configgen"
	"github.com/abottVU/netbox-failover/internal/server/crypto"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/abottVU/netbox-failover/internal/server/hub"
	"github.com/abottVU/netbox-failover/internal/server/sse"
	"github.com/abottVU/netbox-failover/internal/shared/protocol"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// ConfigHandler handles config version CRUD, preview, and push.
type ConfigHandler struct {
	configs     *queries.ConfigQuerier
	taskResults *queries.TaskResultQuerier
	nodes       *queries.NodeQuerier
	clusters    *queries.ClusterQuerier
	creds       *queries.CredentialQuerier
	enc         *crypto.Encryptor
	dispatcher  *hub.Dispatcher
	broker      *sse.Broker
}

func NewConfigHandler(
	configs *queries.ConfigQuerier,
	taskResults *queries.TaskResultQuerier,
	nodes *queries.NodeQuerier,
	clusters *queries.ClusterQuerier,
	creds *queries.CredentialQuerier,
	enc *crypto.Encryptor,
	dispatcher *hub.Dispatcher,
	broker *sse.Broker,
) *ConfigHandler {
	return &ConfigHandler{
		configs:     configs,
		taskResults: taskResults,
		nodes:       nodes,
		clusters:    clusters,
		creds:       creds,
		enc:         enc,
		dispatcher:  dispatcher,
		broker:      broker,
	}
}

// ── Response types ────────────────────────────────────────────────────────────

type configResponse struct {
	ID             string  `json:"id"`
	ClusterID      string  `json:"cluster_id"`
	Version        int     `json:"version"`
	ConfigTemplate string  `json:"config_template"`
	RenderedHash   *string `json:"rendered_hash,omitempty"`
	PushedAt       *string `json:"pushed_at,omitempty"`
	PushStatus     *string `json:"push_status,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

func toConfigResponse(c *queries.NetboxConfig) configResponse {
	r := configResponse{
		ID:             c.ID.String(),
		ClusterID:      c.ClusterID.String(),
		Version:        c.Version,
		ConfigTemplate: c.ConfigTemplate,
		RenderedHash:   c.RenderedHash,
		PushStatus:     c.PushStatus,
		CreatedAt:      c.CreatedAt.Format(time.RFC3339),
	}
	if c.PushedAt != nil {
		s := c.PushedAt.Format(time.RFC3339)
		r.PushedAt = &s
	}
	return r
}

type overrideResp struct {
	ConfigID string `json:"config_id"`
	NodeID   string `json:"node_id"`
	Key      string `json:"key"`
	Value    string `json:"value"`
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// GetOrCreate returns the latest config for a cluster, creating a default if none exists.
// GET /api/v1/clusters/:id/config
func (h *ConfigHandler) GetOrCreate(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	cfg, err := h.configs.GetLatest(ctx, clusterID)
	if err != nil {
		// None yet — ensure cluster exists, then auto-create
		if _, cerr := h.clusters.GetByID(ctx, clusterID); cerr != nil {
			return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
		}
		cfg, err = h.configs.Create(ctx, clusterID, configgen.DefaultTemplate())
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to create default config")
		}
	}

	overrides, _ := h.configs.ListOverrides(ctx, cfg.ID)
	overrideSlice := make([]overrideResp, 0, len(overrides))
	for _, o := range overrides {
		overrideSlice = append(overrideSlice, overrideResp{
			ConfigID: o.ConfigID.String(),
			NodeID:   o.NodeID.String(),
			Key:      o.Key,
			Value:    o.Value,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"config":    toConfigResponse(cfg),
		"overrides": overrideSlice,
	})
}

// Save creates a new config version with an updated template.
// POST /api/v1/clusters/:id/config
func (h *ConfigHandler) Save(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		ConfigTemplate string `json:"config_template"`
	}
	if err := c.Bind(&req); err != nil || req.ConfigTemplate == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "config_template is required")
	}

	cfg, err := h.configs.Create(c.Request().Context(), clusterID, req.ConfigTemplate)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save config")
	}
	return c.JSON(http.StatusCreated, toConfigResponse(cfg))
}

// Preview renders the config for a specific node without pushing.
// POST /api/v1/clusters/:id/config/preview
func (h *ConfigHandler) Preview(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		NodeID         string `json:"node_id"`
		ConfigTemplate string `json:"config_template"`
	}
	_ = c.Bind(&req)

	ctx := c.Request().Context()

	tmplSrc := req.ConfigTemplate
	if tmplSrc == "" {
		cfg, err := h.configs.GetLatest(ctx, clusterID)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "no config saved yet")
		}
		tmplSrc = cfg.ConfigTemplate
	}

	var nodeID uuid.UUID
	if req.NodeID != "" {
		nodeID, err = uuid.Parse(req.NodeID)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid node_id")
		}
	}

	input, err := h.buildRenderInput(ctx, clusterID, nodeID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	content, sha256hex, err := configgen.Render(tmplSrc, input)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "template render error: "+err.Error())
	}

	return c.JSON(http.StatusOK, map[string]any{
		"content":    content,
		"sha256":     sha256hex,
		"char_count": len(content),
	})
}

// Push dispatches config.write tasks to all nodes in the cluster.
// POST /api/v1/clusters/:id/config/:ver/push
func (h *ConfigHandler) Push(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ver, err := strconv.Atoi(c.Param("ver"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid version")
	}

	var req struct {
		RestartAfter bool `json:"restart_after"`
	}
	_ = c.Bind(&req)

	ctx := c.Request().Context()

	cfg, err := h.configs.GetByVersion(ctx, clusterID, ver)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "config version not found")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	type nodeResult struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"` // dispatched | offline | error
		Error    string `json:"error,omitempty"`
	}

	overrides, _ := h.configs.ListOverrides(ctx, cfg.ID)
	results := make([]nodeResult, 0, len(nodes))
	dispatchCount := 0

	for _, node := range nodes {
		nr := nodeResult{NodeID: node.ID.String(), Hostname: node.Hostname}

		input, err := h.buildRenderInput(ctx, clusterID, node.ID)
		if err != nil {
			nr.Status = "error"
			nr.Error = err.Error()
			results = append(results, nr)
			continue
		}

		content, sha256hex, err := configgen.Render(cfg.ConfigTemplate, input)
		if err != nil {
			nr.Status = "error"
			nr.Error = "render error: " + err.Error()
			results = append(results, nr)
			continue
		}

		// Append per-node overrides
		var sb strings.Builder
		sb.WriteString(content)
		for _, ov := range overrides {
			if ov.NodeID == node.ID {
				sb.WriteString(fmt.Sprintf("\n%s = %s\n", ov.Key, ov.Value))
			}
		}
		finalContent := sb.String()

		taskID := uuid.New()
		params, _ := json.Marshal(protocol.ConfigWriteParams{
			Content:        finalContent,
			Sha256:         sha256hex,
			BackupExisting: true,
			RestartAfter:   req.RestartAfter,
		})

		_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskWriteConfig), params)

		if dispErr := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskWriteConfig,
			Params:      json.RawMessage(params),
			TimeoutSecs: 60,
		}); dispErr != nil {
			nr.Status = "offline"
			nr.Error = dispErr.Error()
			results = append(results, nr)
			continue
		}

		_ = h.taskResults.SetSent(ctx, taskID)
		nr.TaskID = taskID.String()
		nr.Status = "dispatched"
		dispatchCount++
		results = append(results, nr)
	}

	overallStatus := "success"
	if dispatchCount == 0 {
		overallStatus = "failed"
	} else if dispatchCount < len(nodes) {
		overallStatus = "partial"
	}
	_ = h.configs.UpdatePushStatus(ctx, cfg.ID, overallStatus, "")

	return c.JSON(http.StatusOK, map[string]any{
		"config_id": cfg.ID.String(),
		"version":   cfg.Version,
		"status":    overallStatus,
		"nodes":     results,
	})
}

// PushStatus returns push state for a config version.
// GET /api/v1/clusters/:id/config/:ver/push-status
func (h *ConfigHandler) PushStatus(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ver, err := strconv.Atoi(c.Param("ver"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid version")
	}

	cfg, err := h.configs.GetByVersion(c.Request().Context(), clusterID, ver)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "config version not found")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"config_id":   cfg.ID.String(),
		"version":     cfg.Version,
		"push_status": cfg.PushStatus,
		"pushed_at":   cfg.PushedAt,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildRenderInput gathers decrypted credentials and node network info.
func (h *ConfigHandler) buildRenderInput(ctx context.Context, clusterID, nodeID uuid.UUID) (configgen.RenderInput, error) {
	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return configgen.RenderInput{}, fmt.Errorf("cluster not found")
	}

	secretKey, err := h.enc.Decrypt(cluster.NetboxSecretKey)
	if err != nil {
		return configgen.RenderInput{}, fmt.Errorf("decrypting secret key: %w", err)
	}
	apiPepper, err := h.enc.Decrypt(cluster.APITokenPepper)
	if err != nil {
		return configgen.RenderInput{}, fmt.Errorf("decrypting api pepper: %w", err)
	}

	dbName, dbUser, dbPassword, redisPassword := "netbox", "netbox", "", ""

	if cred, err := h.creds.GetByKind(ctx, clusterID, "netbox_db_user"); err == nil {
		dbUser = cred.Username
		if cred.DBName != nil {
			dbName = *cred.DBName
		}
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			dbPassword = string(pw)
		}
	}

	if cred, err := h.creds.GetByKind(ctx, clusterID, "redis_password"); err == nil {
		if pw, err := h.enc.Decrypt(cred.PasswordEnc); err == nil {
			redisPassword = string(pw)
		}
	}

	allNodes, _ := h.nodes.ListByCluster(ctx, clusterID)

	// Derive Redis Sentinel addresses from hyperconverged and app nodes (port 26379).
	// If none found, configgen will default to 127.0.0.1:26379.
	var sentinelAddrs []string
	for _, n := range allNodes {
		if n.Role == "hyperconverged" || n.Role == "app" {
			sentinelAddrs = append(sentinelAddrs, stripCIDR(n.IPAddress)+":26379")
		}
	}

	var dbHost string
	var allowedHosts []string

	if nodeID != uuid.Nil {
		node, err := h.nodes.GetByID(ctx, nodeID)
		if err == nil {
			dbHost = stripCIDR(node.IPAddress)
			allowedHosts = []string{node.Hostname, stripCIDR(node.IPAddress)}
		}
	} else if len(allNodes) > 0 {
		dbHost = stripCIDR(allNodes[0].IPAddress)
		allowedHosts = []string{allNodes[0].Hostname, stripCIDR(allNodes[0].IPAddress)}
	}

	if cluster.VIP != nil {
		allowedHosts = append(allowedHosts, *cluster.VIP)
	}

	return configgen.RenderInput{
		SecretKey:      string(secretKey),
		APITokenPepper: string(apiPepper),
		DBHost:         dbHost,
		DBPort:         5432,
		DBName:         dbName,
		DBUser:         dbUser,
		DBPassword:     dbPassword,
		AllowedHosts:   allowedHosts,
		SentinelAddrs:  sentinelAddrs,
		PatroniScope:   cluster.PatroniScope,
		RedisPassword:  redisPassword,
		NetboxVersion:  cluster.NetboxVersion,
	}, nil
}

func stripCIDR(ip string) string {
	if i := strings.IndexByte(ip, '/'); i != -1 {
		return ip[:i]
	}
	return ip
}
