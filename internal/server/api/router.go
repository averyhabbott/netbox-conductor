package api

import (
	"io/fs"
	"net/http"

	"github.com/abottVU/netbox-failover/internal/server/api/handlers"
	mw "github.com/abottVU/netbox-failover/internal/server/api/middleware"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/abottVU/netbox-failover/internal/server/sse"
	webui "github.com/abottVU/netbox-failover/web"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// RouterConfig holds all dependencies the router needs to wire up handlers.
type RouterConfig struct {
	AuthHandler       *handlers.AuthHandler
	AgentHandler      *handlers.AgentHandler
	ClusterHandler    *handlers.ClusterHandler
	NodeHandler       *handlers.NodeHandler
	CredentialHandler *handlers.CredentialHandler
	ConfigHandler     *handlers.ConfigHandler
	PatroniHandler    *handlers.PatroniHandler
	TaskResultQuerier *queries.TaskResultQuerier
	SSEBroker         *sse.Broker
	AuditQuerier      *queries.AuditQuerier
	JWTSecret         []byte
}

// New creates and returns a fully configured Echo instance.
func New(cfg RouterConfig) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Global middleware
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Format: `{"time":"${time_rfc3339}","id":"${id}","method":"${method}","uri":"${uri}","status":${status},"latency_ms":${latency_ms}}` + "\n",
	}))
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "DENY",
		HSTSMaxAge:            31536000,
		ContentSecurityPolicy: "default-src 'self' 'unsafe-inline'",
	}))
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
		AllowHeaders:     []string{echo.HeaderAuthorization, echo.HeaderContentType},
		AllowCredentials: false,
	}))

	v1 := e.Group("/api/v1")

	// ── Auth (no JWT required) ──────────────────────────────────────────────
	auth := v1.Group("/auth")
	auth.POST("/login", cfg.AuthHandler.Login)
	auth.POST("/refresh", cfg.AuthHandler.Refresh)
	auth.POST("/logout", cfg.AuthHandler.Logout)

	// ── Protected routes ───────────────────────────────────────────────────
	protected := v1.Group("", mw.JWT(cfg.JWTSecret), mw.Audit(cfg.AuditQuerier))

	protected.GET("/auth/me", cfg.AuthHandler.Me)

	// SSE live event stream
	protected.GET("/events", echo.WrapHandler(cfg.SSEBroker))

	// ── Clusters ────────────────────────────────────────────────────────────
	protected.GET("/clusters", cfg.ClusterHandler.List)
	protected.POST("/clusters", cfg.ClusterHandler.Create, mw.RequireRole("operator"))
	protected.GET("/clusters/:id", cfg.ClusterHandler.Get)
	protected.PATCH("/clusters/:id/failover-settings", cfg.ClusterHandler.UpdateFailoverSettings, mw.RequireRole("operator"))
	protected.DELETE("/clusters/:id", cfg.ClusterHandler.Delete, mw.RequireRole("admin"))
	protected.GET("/clusters/:id/status", cfg.ClusterHandler.Status)

	// ── Nodes ───────────────────────────────────────────────────────────────
	protected.GET("/clusters/:id/nodes", cfg.NodeHandler.List)
	protected.POST("/clusters/:id/nodes", cfg.NodeHandler.Create, mw.RequireRole("operator"))
	protected.GET("/clusters/:id/nodes/:nid", cfg.NodeHandler.Get)
	protected.PUT("/clusters/:id/nodes/:nid", cfg.NodeHandler.Update, mw.RequireRole("operator"))
	protected.DELETE("/clusters/:id/nodes/:nid", cfg.NodeHandler.Delete, mw.RequireRole("admin"))
	protected.GET("/clusters/:id/nodes/:nid/status", cfg.NodeHandler.Status)
	protected.POST("/clusters/:id/nodes/:nid/registration-token", cfg.NodeHandler.GenerateRegToken, mw.RequireRole("operator"))
	protected.GET("/clusters/:id/nodes/:nid/tasks", cfg.NodeHandler.Tasks)
	protected.POST("/clusters/:id/nodes/:nid/start-netbox", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/stop-netbox", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/restart-netbox", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/restart-rq", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))

	// ── Config (Phase 3) ────────────────────────────────────────────────────
	protected.GET("/clusters/:id/config", cfg.ConfigHandler.GetOrCreate)
	protected.POST("/clusters/:id/config", cfg.ConfigHandler.Save, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/config/:ver/push", cfg.ConfigHandler.Push, mw.RequireRole("operator"))
	protected.GET("/clusters/:id/config/:ver/push-status", cfg.ConfigHandler.PushStatus)
	protected.POST("/clusters/:id/config/preview", cfg.ConfigHandler.Preview)

	// ── Patroni ─────────────────────────────────────────────────────────────
	protected.GET("/clusters/:id/patroni/topology", cfg.PatroniHandler.Topology)
	protected.POST("/clusters/:id/patroni/switchover", cfg.PatroniHandler.Switchover, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/patroni/push-config", cfg.PatroniHandler.PushPatroniConfig, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/patroni/witness/start", cfg.PatroniHandler.StartWitness, mw.RequireRole("admin"))
	protected.GET("/clusters/:id/patroni/history", placeholder("patroni history"))

	// ── Credentials ─────────────────────────────────────────────────────────
	protected.GET("/clusters/:id/credentials", cfg.CredentialHandler.List)
	protected.PUT("/clusters/:id/credentials/:kind", cfg.CredentialHandler.Upsert, mw.RequireRole("operator"))

	// ── Audit logs ──────────────────────────────────────────────────────────
	protected.GET("/audit-logs", func(c echo.Context) error {
		logs, err := cfg.AuditQuerier.List(c.Request().Context(), queries.ListAuditParams{Limit: 50})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to list audit logs")
		}
		if logs == nil {
			logs = []queries.AuditLog{}
		}
		return c.JSON(http.StatusOK, logs)
	})

	// ── Task results ────────────────────────────────────────────────────────
	protected.GET("/tasks/:task_id", func(c echo.Context) error {
		taskID, err := uuid.Parse(c.Param("task_id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid task_id")
		}
		t, err := cfg.TaskResultQuerier.GetByTaskID(c.Request().Context(), taskID)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
		return c.JSON(http.StatusOK, t)
	})

	// ── Agent endpoints (no JWT — agent token auth handled in handler) ──────
	v1.POST("/agent/register", cfg.AgentHandler.Register)
	v1.GET("/agent/connect", cfg.AgentHandler.Connect)

	// ── Binary downloads (unauthenticated) ─────────────────────────────────
	e.GET("/api/v1/downloads/agent-linux-amd64", placeholder("download agent amd64"))
	e.GET("/api/v1/downloads/agent-linux-arm64", placeholder("download agent arm64"))

	// Health check
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// ── SPA fallback — serves the embedded React frontend ──────────────────────
	// All non-API, non-health paths are served from the embedded dist/.
	// The sub-filesystem strips the leading "dist/" prefix so that
	// "/assets/foo.js" maps to embed path "dist/assets/foo.js".
	distFS, err := fs.Sub(webui.FS, "dist")
	if err != nil {
		panic("embedded dist/ not found — run 'make build-frontend' before building the server binary")
	}
	spaHandler := http.FileServer(http.FS(distFS))
	e.GET("/*", echo.WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the file doesn't exist in dist/, serve index.html so React Router handles it.
		f, openErr := distFS.Open(r.URL.Path[1:]) // strip leading "/"
		if openErr == nil {
			f.Close()
			spaHandler.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		spaHandler.ServeHTTP(w, r)
	})))

	return e
}

func placeholder(name string) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{
			"message": name + " — not yet implemented",
		})
	}
}
