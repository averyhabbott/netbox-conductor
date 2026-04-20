package handlers

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/averyhabbott/netbox-conductor/internal/server/alerting/transports"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/logging"
)

// AlertHandler serves alert rules, transports, schedules, active states, and system logs.
type AlertHandler struct {
	ruleQ      *queries.AlertRuleQuerier
	transQ     *queries.AlertTransportQuerier
	schedQ     *queries.AlertScheduleQuerier
	stateQ     *queries.AlertStateQuerier
	fireLogQ   *queries.AlertFireLogQuerier
	retentionQ *queries.EventRetentionQuerier
	logDir     string
	logName    string
}

func NewAlertHandler(
	ruleQ *queries.AlertRuleQuerier,
	transQ *queries.AlertTransportQuerier,
	schedQ *queries.AlertScheduleQuerier,
	stateQ *queries.AlertStateQuerier,
	fireLogQ *queries.AlertFireLogQuerier,
	retentionQ *queries.EventRetentionQuerier,
	logDir, logName string,
) *AlertHandler {
	return &AlertHandler{
		ruleQ:      ruleQ,
		transQ:     transQ,
		schedQ:     schedQ,
		stateQ:     stateQ,
		fireLogQ:   fireLogQ,
		retentionQ: retentionQ,
		logDir:     logDir,
		logName:    logName,
	}
}

// ─── Alert Rules ──────────────────────────────────────────────────────────────

func (h *AlertHandler) ListRules(c echo.Context) error {
	rules, err := h.ruleQ.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list rules")
	}
	if rules == nil {
		rules = []queries.AlertRule{}
	}
	return c.JSON(http.StatusOK, rules)
}

func (h *AlertHandler) CreateRule(c echo.Context) error {
	var p queries.AlertRuleParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if p.FireMode == "" {
		p.FireMode = "once"
	}
	if p.MinSeverity == "" {
		p.MinSeverity = "info"
	}
	r, err := h.ruleQ.Create(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create rule")
	}
	return c.JSON(http.StatusCreated, r)
}

func (h *AlertHandler) GetRule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	r, err := h.ruleQ.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "rule not found")
	}
	return c.JSON(http.StatusOK, r)
}

func (h *AlertHandler) UpdateRule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	var p queries.AlertRuleParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	p.Name = strings.TrimSpace(p.Name)
	r, err := h.ruleQ.Update(c.Request().Context(), id, p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update rule")
	}
	return c.JSON(http.StatusOK, r)
}

func (h *AlertHandler) DeleteRule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := h.ruleQ.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete rule")
	}
	return c.NoContent(http.StatusNoContent)
}

// ─── Alert Transports ─────────────────────────────────────────────────────────

func (h *AlertHandler) ListTransports(c echo.Context) error {
	ts, err := h.transQ.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list transports")
	}
	if ts == nil {
		ts = []queries.AlertTransport{}
	}
	return c.JSON(http.StatusOK, ts)
}

func (h *AlertHandler) CreateTransport(c echo.Context) error {
	var p queries.AlertTransportParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if p.Name == "" || p.Type == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name and type are required")
	}
	t, err := h.transQ.Create(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create transport")
	}
	return c.JSON(http.StatusCreated, t)
}

func (h *AlertHandler) GetTransport(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	t, err := h.transQ.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "transport not found")
	}
	return c.JSON(http.StatusOK, t)
}

func (h *AlertHandler) UpdateTransport(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	var p queries.AlertTransportParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	t, err := h.transQ.Update(c.Request().Context(), id, p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update transport")
	}
	return c.JSON(http.StatusOK, t)
}

func (h *AlertHandler) DeleteTransport(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := h.transQ.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete transport")
	}
	return c.NoContent(http.StatusNoContent)
}

// TestTransport sends a test notification via the given transport.
// POST /api/v1/alerts/transports/:id/test
func (h *AlertHandler) TestTransport(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	t, err := h.transQ.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "transport not found")
	}
	switch t.Type {
	case "webhook":
		err = transports.TestWebhook(t.Config)
	case "email":
		err = transports.TestEmail(t.Config)
	case "slack_webhook":
		err = transports.TestSlackWebhook(t.Config)
	case "slack_bot":
		err = transports.TestSlackBot(t.Config)
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown transport type")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "test failed: "+err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ─── Alert Schedules ──────────────────────────────────────────────────────────

func (h *AlertHandler) ListSchedules(c echo.Context) error {
	ss, err := h.schedQ.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list schedules")
	}
	if ss == nil {
		ss = []queries.AlertSchedule{}
	}
	return c.JSON(http.StatusOK, ss)
}

func (h *AlertHandler) CreateSchedule(c echo.Context) error {
	var p queries.AlertScheduleParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if p.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	s, err := h.schedQ.Create(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create schedule")
	}
	return c.JSON(http.StatusCreated, s)
}

func (h *AlertHandler) GetSchedule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	s, err := h.schedQ.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}
	return c.JSON(http.StatusOK, s)
}

func (h *AlertHandler) UpdateSchedule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	var p queries.AlertScheduleParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	s, err := h.schedQ.Update(c.Request().Context(), id, p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update schedule")
	}
	return c.JSON(http.StatusOK, s)
}

func (h *AlertHandler) DeleteSchedule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := h.schedQ.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete schedule")
	}
	return c.NoContent(http.StatusNoContent)
}

// ─── Active Alert States ──────────────────────────────────────────────────────

func (h *AlertHandler) ListActiveAlerts(c echo.Context) error {
	states, err := h.stateQ.ListActive(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list active alerts")
	}
	if states == nil {
		states = []queries.AlertState{}
	}
	return c.JSON(http.StatusOK, states)
}

// ResolveAlert marks an active alert state as resolved.
// POST /api/v1/alerts/active/:id/resolve
func (h *AlertHandler) ResolveAlert(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid alert state id")
	}
	if err := h.stateQ.ResolveByID(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to resolve alert")
	}
	return c.NoContent(http.StatusNoContent)
}

// AcknowledgeAlert marks an active alert state as acknowledged.
// POST /api/v1/alerts/active/:id/acknowledge
func (h *AlertHandler) AcknowledgeAlert(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid alert state id")
	}
	// User ID from JWT context — use nil UUID as fallback.
	userID := uuid.Nil
	if uid, ok := c.Get("user_id").(uuid.UUID); ok {
		userID = uid
	}
	if err := h.stateQ.Acknowledge(c.Request().Context(), id, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to acknowledge alert")
	}
	return c.NoContent(http.StatusNoContent)
}

// ListFireLog returns the alert fire history for the last 30 days.
// GET /api/v1/alerts/history
func (h *AlertHandler) ListFireLog(c echo.Context) error {
	entries, err := h.fireLogQ.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list alert history")
	}
	if entries == nil {
		entries = []queries.AlertFireLog{}
	}
	return c.JSON(http.StatusOK, entries)
}

// ─── Event Retention ──────────────────────────────────────────────────────────

func (h *AlertHandler) GetRetention(c echo.Context) error {
	policies, err := h.retentionQ.GetAll(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get retention policies")
	}
	if policies == nil {
		policies = []queries.EventRetentionPolicy{}
	}
	return c.JSON(http.StatusOK, policies)
}

func (h *AlertHandler) UpdateRetention(c echo.Context) error {
	var policies []queries.EventRetentionPolicy
	if err := c.Bind(&policies); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := h.retentionQ.UpdateAll(c.Request().Context(), policies); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update retention policies")
	}
	return c.NoContent(http.StatusNoContent)
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
	path := filepath.Join(h.logDir, h.logName, "conductor.log")
	lines, err := logging.TailFile(path, n)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read system logs")
	}
	return c.JSON(http.StatusOK, map[string]any{"lines": lines})
}
