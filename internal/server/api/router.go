package api

import (
	"encoding/csv"
	"fmt"
	"io/fs"
	"net"
	"net/http"

	"github.com/averyhabbott/netbox-conductor/internal/server/api/handlers"
	mw "github.com/averyhabbott/netbox-conductor/internal/server/api/middleware"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	webui "github.com/averyhabbott/netbox-conductor/web"
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
	DownloadHandler   *handlers.DownloadHandler
	StagingHandler    *handlers.StagingHandler
	MetricsHandler    *handlers.MetricsHandler
	AlertHandler      *handlers.AlertHandler
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
	auth.POST("/login", cfg.AuthHandler.Login, mw.LoginRateLimit())
	auth.POST("/refresh", cfg.AuthHandler.Refresh)
	auth.POST("/logout", cfg.AuthHandler.Logout)

	// ── Protected routes ───────────────────────────────────────────────────
	protected := v1.Group("", mw.JWT(cfg.JWTSecret), mw.Audit(cfg.AuditQuerier))

	protected.GET("/auth/me", cfg.AuthHandler.Me)
	protected.POST("/auth/change-password", cfg.AuthHandler.ChangePassword)
	protected.GET("/auth/totp/status", cfg.AuthHandler.TOTPStatus)
	protected.POST("/auth/totp/enroll", cfg.AuthHandler.EnrollTOTP)
	protected.POST("/auth/totp/confirm", cfg.AuthHandler.ConfirmTOTP)
	protected.POST("/auth/totp/disable", cfg.AuthHandler.DisableTOTP)

	// ── TOTP second-step (no JWT — totp_token acts as auth) ──────────────────
	v1.POST("/auth/totp/verify", cfg.AuthHandler.VerifyTOTP)

	// ── Users (admin-only management) ──────────────────────────────────────────
	protected.GET("/users", cfg.AuthHandler.ListUsers, mw.RequireRole("admin"))
	protected.POST("/users", cfg.AuthHandler.CreateUser, mw.RequireRole("admin"))
	protected.PATCH("/users/:id/role", cfg.AuthHandler.UpdateUserRole, mw.RequireRole("admin"))
	protected.DELETE("/users/:id", cfg.AuthHandler.DeleteUser, mw.RequireRole("admin"))

	// ── Settings ────────────────────────────────────────────────────────────────
	protected.GET("/settings/tls", cfg.AuthHandler.TLSInfo)

	// SSE live event stream
	protected.GET("/events", echo.WrapHandler(cfg.SSEBroker))

	// ── Clusters ────────────────────────────────────────────────────────────
	protected.GET("/clusters", cfg.ClusterHandler.List)
	protected.POST("/clusters", cfg.ClusterHandler.Create, mw.RequireRole("operator"))
	protected.GET("/clusters/:id", cfg.ClusterHandler.Get)
	protected.PATCH("/clusters/:id/failover-settings", cfg.ClusterHandler.UpdateFailoverSettings, mw.RequireRole("operator"))
	protected.PATCH("/clusters/:id/media-sync-settings", cfg.ClusterHandler.UpdateMediaSyncSettings, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/media-sync", cfg.AgentHandler.ClusterMediaSync, mw.RequireRole("operator"))
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
	protected.POST("/clusters/:id/nodes/:nid/agent-env", cfg.NodeHandler.DownloadAgentEnv, mw.RequireRole("operator"))
	protected.GET("/clusters/:id/nodes/:nid/tasks", cfg.NodeHandler.Tasks)
	protected.POST("/clusters/:id/nodes/:nid/media-sync", cfg.AgentHandler.StartMediaSync, mw.RequireRole("operator"))
	protected.GET("/clusters/:id/nodes/:nid/logs", cfg.NodeHandler.GetLogs)
	protected.GET("/clusters/:id/nodes/:nid/netbox-log-names", cfg.NodeHandler.GetNetboxLogNames)
	protected.PUT("/clusters/:id/nodes/:nid/maintenance", cfg.NodeHandler.SetMaintenance, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/start-netbox", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/stop-netbox", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/restart-netbox", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/restart-rq", cfg.NodeHandler.ServiceAction, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/upgrade-agent", cfg.NodeHandler.UpgradeAgent, mw.RequireRole("operator"))

	// ── Config (Phase 3) ────────────────────────────────────────────────────
	protected.GET("/clusters/:id/config", cfg.ConfigHandler.GetOrCreate)
	protected.POST("/clusters/:id/config", cfg.ConfigHandler.Save, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/config/:ver/push", cfg.ConfigHandler.Push, mw.RequireRole("operator"))
	protected.GET("/clusters/:id/config/:ver/push-status", cfg.ConfigHandler.PushStatus)
	protected.POST("/clusters/:id/config/preview", cfg.ConfigHandler.Preview)

	// ── Patroni ─────────────────────────────────────────────────────────────
	protected.GET("/clusters/:id/patroni/topology", cfg.PatroniHandler.Topology)
	protected.POST("/clusters/:id/patroni/switchover", cfg.PatroniHandler.Switchover, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/patroni/failover", cfg.PatroniHandler.Failover, mw.RequireRole("admin"))
	protected.POST("/clusters/:id/configure-failover", cfg.PatroniHandler.ConfigureFailover, mw.RequireRole("admin"))
	protected.GET("/clusters/:id/failover-events", cfg.PatroniHandler.ListFailoverEvents)
	protected.POST("/clusters/:id/patroni/push-config", cfg.PatroniHandler.PushPatroniConfig, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/patroni/witness/start", cfg.PatroniHandler.StartWitness, mw.RequireRole("admin"))
	protected.GET("/clusters/:id/patroni/history", cfg.PatroniHandler.History)
	protected.POST("/clusters/:id/nodes/:nid/push-patroni-config", cfg.PatroniHandler.PushPatroniConfigNode, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/nodes/:nid/install-patroni", cfg.PatroniHandler.InstallPatroni, mw.RequireRole("admin"))
	protected.POST("/clusters/:id/nodes/:nid/db-restore", cfg.PatroniHandler.DBRestore, mw.RequireRole("admin"))

	// ── Retention policy ────────────────────────────────────────────────────
	protected.GET("/clusters/:id/retention-policy", cfg.PatroniHandler.GetRetentionPolicy)
	protected.PUT("/clusters/:id/retention-policy", cfg.PatroniHandler.SetRetentionPolicy, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/retention-policy/enforce", cfg.PatroniHandler.EnforceRetention, mw.RequireRole("operator"))

	// ── Sentinel ────────────────────────────────────────────────────────────────
	protected.POST("/clusters/:id/sentinel/push-config", cfg.PatroniHandler.PushSentinelConfig, mw.RequireRole("operator"))

	// ── Credentials ─────────────────────────────────────────────────────────
	protected.GET("/clusters/:id/credentials", cfg.CredentialHandler.List)
	protected.PUT("/clusters/:id/credentials/:kind", cfg.CredentialHandler.Upsert, mw.RequireRole("operator"))
	protected.POST("/clusters/:id/credentials/generate", cfg.CredentialHandler.GenerateCredentials, mw.RequireRole("operator"))

	// ── Alerts ──────────────────────────────────────────────────────────────────
	if cfg.AlertHandler != nil {
		protected.GET("/alerts", cfg.AlertHandler.ListActiveAlerts)
		protected.POST("/alerts/:id/acknowledge", cfg.AlertHandler.AcknowledgeAlert, mw.RequireRole("operator"))
		protected.GET("/alert-configs", cfg.AlertHandler.ListAlertConfigs)
		protected.POST("/alert-configs", cfg.AlertHandler.CreateAlertConfig, mw.RequireRole("operator"))
		protected.PUT("/alert-configs/:id", cfg.AlertHandler.UpdateAlertConfig, mw.RequireRole("operator"))
		protected.DELETE("/alert-configs/:id", cfg.AlertHandler.DeleteAlertConfig, mw.RequireRole("operator"))
		protected.GET("/clusters/:id/logs", cfg.AlertHandler.ClusterLogs)
		protected.GET("/system/logs", cfg.AlertHandler.SystemLogs, mw.RequireRole("operator"))
	}

	// ── Cluster-scoped audit log ─────────────────────────────────────────────────
	protected.GET("/clusters/:id/audit-logs", func(c echo.Context) error {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
		}
		logs, err := cfg.AuditQuerier.ListByCluster(c.Request().Context(), id, 200)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to list audit logs")
		}
		if logs == nil {
			logs = []queries.AuditLog{}
		}
		return c.JSON(http.StatusOK, logs)
	})

	// ── Audit logs ──────────────────────────────────────────────────────────
	protected.GET("/audit-logs", func(c echo.Context) error {
		limit := 500
		logs, err := cfg.AuditQuerier.List(c.Request().Context(), queries.ListAuditParams{Limit: limit})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to list audit logs")
		}
		if logs == nil {
			logs = []queries.AuditLog{}
		}

		if c.QueryParam("format") == "csv" {
			c.Response().Header().Set("Content-Type", "text/csv")
			c.Response().Header().Set("Content-Disposition", "attachment; filename=\"audit-logs.csv\"")
			w := csv.NewWriter(c.Response().Writer)
			_ = w.Write([]string{"id", "action", "actor_user_id", "actor_agent_node_id", "target_type", "target_id", "outcome", "created_at"})
			for _, l := range logs {
				actorUser := ""
				if l.ActorUserID != nil {
					actorUser = l.ActorUserID.String()
				}
				actorAgent := ""
				if l.ActorAgentNodeID != nil {
					actorAgent = l.ActorAgentNodeID.String()
				}
				targetType := ""
				if l.TargetType != nil {
					targetType = *l.TargetType
				}
				targetID := ""
				if l.TargetID != nil {
					targetID = l.TargetID.String()
				}
				outcome := ""
				if l.Outcome != nil {
					outcome = *l.Outcome
				}
				_ = w.Write([]string{
					fmt.Sprintf("%d", l.ID),
					l.Action,
					actorUser,
					actorAgent,
					targetType,
					targetID,
					outcome,
					l.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
				})
			}
			w.Flush()
			return nil
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

	// ── Staging agents (available / unassigned pool) ────────────────────────
	protected.GET("/staging/tokens", cfg.StagingHandler.ListStagingTokens, mw.RequireRole("operator"))
	protected.POST("/staging/tokens", cfg.StagingHandler.CreateStagingToken, mw.RequireRole("operator"))
	protected.DELETE("/staging/tokens/:id", cfg.StagingHandler.DeleteStagingToken, mw.RequireRole("operator"))
	protected.GET("/staging/agents", cfg.StagingHandler.ListStagingAgents)
	protected.DELETE("/staging/agents/:id", cfg.StagingHandler.DeleteStagingAgent, mw.RequireRole("operator"))
	protected.POST("/staging/agents/:id/assign", cfg.StagingHandler.AssignStagingAgent, mw.RequireRole("operator"))

	// ── Hostname resolution utility ─────────────────────────────────────────
	protected.GET("/resolve", func(c echo.Context) error {
		hostname := c.QueryParam("hostname")
		if hostname == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "hostname is required")
		}
		addrs, err := net.LookupHost(hostname)
		if err != nil || len(addrs) == 0 {
			return echo.NewHTTPError(http.StatusNotFound, "could not resolve hostname")
		}
		return c.JSON(http.StatusOK, map[string]string{"ip": addrs[0]})
	})

	// ── Agent endpoints (no JWT — agent token auth handled in handler) ──────
	v1.POST("/agent/register", cfg.AgentHandler.Register)
	v1.POST("/agent/staging-register", cfg.AgentHandler.StagingRegister)
	v1.GET("/agent/connect", cfg.AgentHandler.Connect)
	v1.GET("/agent/sync-config", cfg.AgentHandler.SyncConfig)

	// ── Binary downloads (unauthenticated) ─────────────────────────────────
	e.GET("/api/v1/downloads/agent-linux-amd64", cfg.DownloadHandler.AgentBinary("amd64"))
	e.GET("/api/v1/downloads/agent-linux-arm64", cfg.DownloadHandler.AgentBinary("arm64"))
	e.GET("/api/v1/downloads/ca.crt", cfg.DownloadHandler.CACert)

	// Health check
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Prometheus metrics
	if cfg.MetricsHandler != nil {
		e.GET("/metrics", cfg.MetricsHandler.Metrics)
	}

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
