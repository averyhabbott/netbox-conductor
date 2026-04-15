package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/labstack/echo/v4"
)

type clusterCounter interface {
	CountClusters(ctx context.Context) (int, error)
}

type nodeCounter interface {
	CountNodes(ctx context.Context) (int, error)
}

// MetricsHandler serves a /metrics endpoint in Prometheus text format.
type MetricsHandler struct {
	hub     *hub.Hub
	clusters clusterCounter
	nodes    nodeCounter
}

// NewMetricsHandler constructs a MetricsHandler.
func NewMetricsHandler(h *hub.Hub, clusters clusterCounter, nodes nodeCounter) *MetricsHandler {
	return &MetricsHandler{hub: h, clusters: clusters, nodes: nodes}
}

// Metrics renders Prometheus-format text metrics.
func (m *MetricsHandler) Metrics(c echo.Context) error {
	connected := m.hub.ConnectedCount()
	staging := m.hub.ConnectedStagingCount()

	var clusterCount, nodeCount int
	if m.clusters != nil {
		clusterCount, _ = m.clusters.CountClusters(c.Request().Context())
	}
	if m.nodes != nil {
		nodeCount, _ = m.nodes.CountNodes(c.Request().Context())
	}

	body := fmt.Sprintf(`# HELP conductor_agents_connected Number of assigned agents currently connected via WebSocket.
# TYPE conductor_agents_connected gauge
conductor_agents_connected %d
# HELP conductor_staging_agents_connected Number of unassigned (staging) agents currently connected.
# TYPE conductor_staging_agents_connected gauge
conductor_staging_agents_connected %d
# HELP conductor_clusters_total Total number of clusters registered.
# TYPE conductor_clusters_total gauge
conductor_clusters_total %d
# HELP conductor_nodes_total Total number of nodes registered across all clusters.
# TYPE conductor_nodes_total gauge
conductor_nodes_total %d
`, connected, staging, clusterCount, nodeCount)

	c.Response().Header().Set(echo.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
	return c.String(http.StatusOK, body)
}
