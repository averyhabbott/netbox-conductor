package handlers

import (
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/logging"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// AlertHandler serves alert configs, active alerts, cluster logs, and system logs.
type AlertHandler struct {
	alertQ    *queries.AlertQuerier
	nodeLogQ  *queries.NodeLogQuerier
	logDir    string
	logName   string
}

func NewAlertHandler(
	alertQ *queries.AlertQuerier,
	nodeLogQ *queries.NodeLogQuerier,
	logDir, logName string,
) *AlertHandler {
	return &AlertHandler{
		alertQ:   alertQ,
		nodeLogQ: nodeLogQ,
		logDir:   logDir,
		logName:  logName,
	}
}

// ─── Active Alerts ────────────────────────────────────────────────────────────

// ListActiveAlerts returns all unresolved alerts.
// GET /api/v1/alerts
func (h *AlertHandler) ListActiveAlerts(c echo.Context) error {
	alerts, err := h.alertQ.ListActive(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list alerts")
	}
	if alerts == nil {
		alerts = []queries.ActiveAlert{}
	}
	return c.JSON(http.StatusOK, alerts)
}

// AcknowledgeAlert marks a single alert as acknowledged.
// POST /api/v1/alerts/:id/acknowledge
func (h *AlertHandler) AcknowledgeAlert(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid alert id")
	}
	if err := h.alertQ.AcknowledgeAlert(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to acknowledge alert")
	}
	return c.NoContent(http.StatusNoContent)
}

// ─── Alert Configs ────────────────────────────────────────────────────────────

// ListAlertConfigs returns all alert configurations.
// GET /api/v1/alert-configs
func (h *AlertHandler) ListAlertConfigs(c echo.Context) error {
	configs, err := h.alertQ.ListConfigs(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list alert configs")
	}
	if configs == nil {
		configs = []queries.AlertConfig{}
	}
	return c.JSON(http.StatusOK, configs)
}

type alertConfigBody struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Enabled    bool     `json:"enabled"`
	Conditions []string `json:"conditions"`
	WebhookURL *string  `json:"webhook_url"`
	EmailTo    *string  `json:"email_to"`
}

// CreateAlertConfig creates a new alert configuration.
// POST /api/v1/alert-configs
func (h *AlertHandler) CreateAlertConfig(c echo.Context) error {
	var body alertConfigBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if body.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if body.Type != "webhook" && body.Type != "email" {
		return echo.NewHTTPError(http.StatusBadRequest, "type must be 'webhook' or 'email'")
	}
	cfg, err := h.alertQ.CreateConfig(c.Request().Context(), queries.UpsertAlertConfigParams{
		Name:       body.Name,
		Type:       body.Type,
		Enabled:    body.Enabled,
		Conditions: body.Conditions,
		WebhookURL: body.WebhookURL,
		EmailTo:    body.EmailTo,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create alert config")
	}
	return c.JSON(http.StatusCreated, cfg)
}

// UpdateAlertConfig updates an existing alert configuration.
// PUT /api/v1/alert-configs/:id
func (h *AlertHandler) UpdateAlertConfig(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid config id")
	}
	var body alertConfigBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	cfg, err := h.alertQ.UpdateConfig(c.Request().Context(), id, queries.UpsertAlertConfigParams{
		Name:       body.Name,
		Type:       body.Type,
		Enabled:    body.Enabled,
		Conditions: body.Conditions,
		WebhookURL: body.WebhookURL,
		EmailTo:    body.EmailTo,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update alert config")
	}
	return c.JSON(http.StatusOK, cfg)
}

// DeleteAlertConfig deletes an alert configuration.
// DELETE /api/v1/alert-configs/:id
func (h *AlertHandler) DeleteAlertConfig(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid config id")
	}
	if err := h.alertQ.DeleteConfig(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete alert config")
	}
	return c.NoContent(http.StatusNoContent)
}

// ─── Cluster Logs ─────────────────────────────────────────────────────────────

// ClusterLogs returns structured log entries for a cluster.
// GET /api/v1/clusters/:id/logs?level=warn&limit=200
func (h *AlertHandler) ClusterLogs(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	minLevel := c.QueryParam("level") // "", "debug", "info", "warn", "error"
	limit := 200
	if lStr := c.QueryParam("limit"); lStr != "" {
		if n, err := strconv.Atoi(lStr); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := h.nodeLogQ.ListByCluster(c.Request().Context(), clusterID, minLevel, limit)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list cluster logs")
	}
	if entries == nil {
		entries = []queries.NodeLogEntry{}
	}
	return c.JSON(http.StatusOK, entries)
}

// ─── System Logs ──────────────────────────────────────────────────────────────

// SystemLogs returns the last N lines of the conductor's own log file.
// GET /api/v1/system/logs?lines=200
func (h *AlertHandler) SystemLogs(c echo.Context) error {
	n := 200
	if nStr := c.QueryParam("lines"); nStr != "" {
		if parsed, err := strconv.Atoi(nStr); err == nil && parsed > 0 && parsed <= 2000 {
			n = parsed
		}
	}
	// conductor.log lives at logDir/logName/conductor.log
	path := filepath.Join(h.logDir, h.logName, "conductor.log")
	lines, err := logging.TailFile(path, n)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read system logs")
	}
	return c.JSON(http.StatusOK, map[string]any{"lines": lines})
}
