package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
)

// EventsHandler serves the structured event log API.
type EventsHandler struct {
	eventQ *queries.EventQuerier
	hbQ    *queries.HeartbeatQuerier
}

func NewEventsHandler(eventQ *queries.EventQuerier, hbQ *queries.HeartbeatQuerier) *EventsHandler {
	return &EventsHandler{eventQ: eventQ, hbQ: hbQ}
}

// ListEvents returns events matching the given filter.
// GET /api/v1/events?category=&severity=&code=&cluster_id=&node_id=&from=&to=&limit=&offset=
func (h *EventsHandler) ListEvents(c echo.Context) error {
	f := queries.EventFilter{
		Category: c.QueryParam("category"),
		Code:     c.QueryParam("code"),
		Severity: c.QueryParam("severity"),
	}
	if s := c.QueryParam("cluster_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster_id")
		}
		f.ClusterID = &id
	}
	if s := c.QueryParam("node_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid node_id")
		}
		f.NodeID = &id
	}
	if s := c.QueryParam("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid from timestamp (RFC3339 required)")
		}
		f.From = &t
	}
	if s := c.QueryParam("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid to timestamp (RFC3339 required)")
		}
		f.To = &t
	}
	parseIntParam(c, "limit", &f.Limit, 200, 1, 1000)
	parseIntParam(c, "offset", &f.Offset, 0, 0, 1<<30)

	evs, err := h.eventQ.List(c.Request().Context(), f)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list events")
	}
	if evs == nil {
		return c.JSON(http.StatusOK, []struct{}{})
	}
	return c.JSON(http.StatusOK, evs)
}

// ListClusterEvents returns events scoped to a specific cluster.
// GET /api/v1/clusters/:id/events
func (h *EventsHandler) ListClusterEvents(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	f := queries.EventFilter{
		Category:  c.QueryParam("category"),
		Severity:  c.QueryParam("severity"),
		ClusterID: &clusterID,
	}
	parseIntParam(c, "limit", &f.Limit, 200, 1, 1000)
	evs, err := h.eventQ.List(c.Request().Context(), f)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list events")
	}
	if evs == nil {
		return c.JSON(http.StatusOK, []struct{}{})
	}
	return c.JSON(http.StatusOK, evs)
}

// ListNodeEvents returns events scoped to a specific node.
// GET /api/v1/clusters/:id/nodes/:nid/events
func (h *EventsHandler) ListNodeEvents(c echo.Context) error {
	nodeID, err := uuid.Parse(c.Param("nid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node id")
	}
	f := queries.EventFilter{
		Category: c.QueryParam("category"),
		Severity: c.QueryParam("severity"),
		NodeID:   &nodeID,
	}
	parseIntParam(c, "limit", &f.Limit, 200, 1, 1000)
	evs, err := h.eventQ.List(c.Request().Context(), f)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list events")
	}
	if evs == nil {
		return c.JSON(http.StatusOK, []struct{}{})
	}
	return c.JSON(http.StatusOK, evs)
}

// ListHeartbeats returns time-series heartbeats for a node.
// GET /api/v1/heartbeats?node_id=&from=&to=&limit=
func (h *EventsHandler) ListHeartbeats(c echo.Context) error {
	s := c.QueryParam("node_id")
	if s == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "node_id is required")
	}
	nodeID, err := uuid.Parse(s)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid node_id")
	}
	var from, to *time.Time
	if s := c.QueryParam("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid from")
		}
		from = &t
	}
	if s := c.QueryParam("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid to")
		}
		to = &t
	}
	limit := 500
	parseIntParam(c, "limit", &limit, 500, 1, 5000)

	hbs, err := h.hbQ.List(c.Request().Context(), nodeID, from, to, limit)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list heartbeats")
	}
	if hbs == nil {
		return c.JSON(http.StatusOK, []struct{}{})
	}
	return c.JSON(http.StatusOK, hbs)
}

// parseIntParam reads a query param into *target, applying min/max bounds and a default.
func parseIntParam(c echo.Context, name string, target *int, def, min, max int) {
	*target = def
	if s := c.QueryParam(name); s != "" {
		var n int
		if _, err := parseIntStr(s, &n); err == nil {
			if n < min {
				n = min
			} else if n > max {
				n = max
			}
			*target = n
		}
	}
}

func parseIntStr(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, &parseIntError{}
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return n, nil
}

type parseIntError struct{}

func (e *parseIntError) Error() string { return "not an integer" }
