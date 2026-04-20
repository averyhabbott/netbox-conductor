package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	syslogfwd "github.com/averyhabbott/netbox-conductor/internal/server/syslog"
)

// SyslogHandler serves syslog destination CRUD.
type SyslogHandler struct {
	destQ *queries.SyslogDestinationQuerier
}

func NewSyslogHandler(destQ *queries.SyslogDestinationQuerier) *SyslogHandler {
	return &SyslogHandler{destQ: destQ}
}

func (h *SyslogHandler) List(c echo.Context) error {
	dests, err := h.destQ.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list syslog destinations")
	}
	if dests == nil {
		dests = []queries.SyslogDestination{}
	}
	return c.JSON(http.StatusOK, dests)
}

func (h *SyslogHandler) Create(c echo.Context) error {
	var p queries.SyslogDestinationParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if p.Host == "" || p.Protocol == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "host and protocol are required")
	}
	if p.Port == 0 {
		p.Port = 514
	}
	d, err := h.destQ.Create(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create destination")
	}
	return c.JSON(http.StatusCreated, d)
}

func (h *SyslogHandler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	d, err := h.destQ.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "destination not found")
	}
	return c.JSON(http.StatusOK, d)
}

func (h *SyslogHandler) Update(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	var p queries.SyslogDestinationParams
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	d, err := h.destQ.Update(c.Request().Context(), id, p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update destination")
	}
	return c.JSON(http.StatusOK, d)
}

func (h *SyslogHandler) Delete(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := h.destQ.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete destination")
	}
	return c.NoContent(http.StatusNoContent)
}

// TestDest sends a test RFC 5424 message to the syslog destination.
// POST /api/v1/syslog/destinations/:id/test
func (h *SyslogHandler) TestDest(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	d, err := h.destQ.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "destination not found")
	}

	testEvent := events.Event{
		Category:   events.CategoryAgent,
		Severity:   events.SeverityInfo,
		Code:       "NBC-TEST",
		Message:    "Test message from NetBox Conductor syslog forwarder",
		Actor:      actorFromCtx(c),
		OccurredAt: time.Now().UTC(),
	}
	msg := syslogfwd.Format(testEvent)
	dest := syslogfwd.NewDestination(*d)
	dest.Send(msg)
	dest.Close()

	return c.JSON(http.StatusOK, map[string]string{"status": "sent", "message": msg})
}
