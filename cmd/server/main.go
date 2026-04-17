package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/alerting"
	"github.com/averyhabbott/netbox-conductor/internal/server/api"
	"github.com/averyhabbott/netbox-conductor/internal/server/api/handlers"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db"
	dbmigrations "github.com/averyhabbott/netbox-conductor/internal/server/db/migrations"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/failover"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/logging"
	"github.com/averyhabbott/netbox-conductor/internal/server/patroni"
	"github.com/averyhabbott/netbox-conductor/internal/server/scheduler"
	"github.com/averyhabbott/netbox-conductor/internal/server/sse"
	"github.com/averyhabbott/netbox-conductor/internal/server/tlscert"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func run(ctx context.Context) error {
	dsn := requireEnv("DATABASE_URL")
	dbPassword := requireEnv("DB_PASSWORD")
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_URL: %w", err)
	}
	u.User = url.UserPassword(u.User.Username(), dbPassword)
	dsn = u.String()
	jwtSecret := []byte(requireEnv("JWT_SECRET"))
	addr := envOr("LISTEN_ADDR", ":8443")
	serverBindIP := envOr("SERVER_BIND_IP", "")
	serverURL := envOr("SERVER_URL", "")

	// Validate SERVER_BIND_IP — must be a parseable IP address, never a hostname,
	// because it is written directly into Patroni configs as the witness Raft
	// address that data nodes connect to.
	if serverBindIP != "" {
		if net.ParseIP(serverBindIP) == nil {
			return fmt.Errorf("SERVER_BIND_IP %q is not a valid IP address — hostnames are not allowed", serverBindIP)
		}
	}

	// Derive SERVER_URL from SERVER_BIND_IP when not explicitly set, so agents
	// get a working WebSocket URL without requiring duplicate configuration.
	if serverURL == "" && serverBindIP != "" {
		serverURL = "https://" + serverBindIP
	}

	// Append the port from LISTEN_ADDR to SERVER_URL when the URL has no
	// explicit port and the port is non-standard.
	if serverURL != "" {
		if u, err := url.Parse(serverURL); err == nil && u.Port() == "" {
			if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
				isDefault := (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")
				if !isDefault {
					u.Host = u.Hostname() + ":" + port
					serverURL = u.String()
				}
			}
		}
	}
	logDir := envOr("LOG_DIR", "/var/log")
	logName := envOr("LOG_NAME", "netbox-conductor")
	logLevel := envOr("LOG_LEVEL", "info")
	agentBinDir := envOr("AGENT_BIN_DIR", "/var/lib/netbox-conductor/bin") // directory holding pre-built agent binaries
	tlsCertFile := envOr("TLS_CERT_FILE", "/etc/netbox-conductor/tls.crt")
	tlsKeyFile := envOr("TLS_KEY_FILE", "/etc/netbox-conductor/tls.key")

	// Structured logging — routes all log.Printf calls through slog at Info level.
	logger := logging.Setup(logDir, logName, logLevel)
	slog.SetDefault(logger)

	// TLS — generate self-signed ECDSA cert on first run (or when expiring).
	// Falls back to plain HTTP if the cert directory is not writable (e.g. dev).
	dnsNames, ipAddrs := tlscert.SANsFromServerURL(serverURL)
	if generated, err := tlscert.EnsureExists(tlsCertFile, tlsKeyFile, dnsNames, ipAddrs); err != nil {
		slog.Warn("TLS cert generation failed — falling back to plain HTTP (not recommended for production)", "error", err)
		tlsCertFile = ""
		tlsKeyFile = ""
	} else if generated {
		slog.Info("generated new TLS certificate", "cert", tlsCertFile)
	} else {
		slog.Info("TLS certificate loaded", "cert", tlsCertFile)
	}

	// Master encryption key
	mk, err := crypto.LoadMasterKey(true)
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	enc := crypto.NewEncryptor(mk)

	// Database
	store, err := db.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer store.Close()
	log.Println("database connected")

	// Migrations
	if err := runMigrations(dsn); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	log.Println("migrations applied")

	// Queriers
	userQ := queries.NewUserQuerier(store.Pool())
	refreshQ := queries.NewRefreshTokenQuerier(store.Pool())
	nodeQ := queries.NewNodeQuerier(store.Pool())
	agentTokQ := queries.NewAgentTokenQuerier(store.Pool())
	regTokQ := queries.NewRegistrationTokenQuerier(store.Pool())
	stagingTokQ := queries.NewStagingTokenQuerier(store.Pool())
	stagingAgentQ := queries.NewStagingAgentQuerier(store.Pool())
	retentionQ := queries.NewRetentionQuerier(store.Pool())
	clusterQ := queries.NewClusterQuerier(store.Pool())
	credQ := queries.NewCredentialQuerier(store.Pool())
	auditQ := queries.NewAuditQuerier(store.Pool())
	configQ := queries.NewConfigQuerier(store.Pool())
	taskQ := queries.NewTaskResultQuerier(store.Pool())
	failoverEventQ := queries.NewFailoverEventQuerier(store.Pool())
	nodeLogQ := queries.NewNodeLogQuerier(store.Pool())
	alertQ := queries.NewAlertQuerier(store.Pool())

	// Seed default admin
	if err := seedAdminIfEmpty(ctx, userQ); err != nil {
		return fmt.Errorf("seeding admin: %w", err)
	}

	// Hub + SSE broker
	h := hub.New()
	dispatcher := hub.NewDispatcher(h)
	broker := sse.New()

	// Background task sweeper — times out stuck tasks
	taskSweeper := scheduler.NewTaskSweeper(taskQ)
	go taskSweeper.Run(ctx)

	// Patroni witness manager
	witnessManager := patroni.NewWitnessManager(patroni.WitnessConfig{
		ServerAddr: serverBindIP,
	})

	// Recover witnesses for any active_standby clusters that were already
	// configured before this conductor process started. Without this, all
	// witness subprocesses die when the conductor restarts and don't
	// restart until configure_failover is manually triggered again.
	go recoverWitnesses(ctx, witnessManager, clusterQ, nodeQ)

	// Failover manager — orchestrates automatic NetBox failover/failback
	failoverManager := failover.New(nodeQ, clusterQ, taskQ, failoverEventQ, h, dispatcher, broker, credQ, enc)

	// Alerting
	alertSender := alerting.New(alertQ)
	alertHandler := handlers.NewAlertHandler(alertQ, nodeLogQ, logDir, logName)

	// Handlers
	authHandler := handlers.NewAuthHandler(userQ, refreshQ, jwtSecret, tlsCertFile, tlsKeyFile, serverURL, enc)
	agentHandler := handlers.NewAgentHandler(h, dispatcher, broker, nodeQ, agentTokQ, regTokQ, stagingTokQ, stagingAgentQ, taskQ, clusterQ, enc, failoverManager, logDir, logName)
	agentHandler.SetNodeLogQuerier(nodeLogQ)
	agentHandler.SetAlertSender(alertSender)
	stagingHandler := handlers.NewStagingHandler(stagingTokQ, stagingAgentQ, nodeQ, agentTokQ, h, broker)
	clusterHandler := handlers.NewClusterHandler(clusterQ, nodeQ, regTokQ, h, enc, witnessManager)
	nodeHandler := handlers.NewNodeHandler(nodeQ, regTokQ, agentTokQ, taskQ, clusterQ, h, dispatcher, broker, serverURL, logDir, logName)
	nodeHandler.SetFailoverManager(failoverManager)
	credHandler := handlers.NewCredentialHandler(credQ, enc)
	downloadHandler := handlers.NewDownloadHandler(agentBinDir, tlsCertFile)
	configHandler := handlers.NewConfigHandler(configQ, taskQ, nodeQ, clusterQ, credQ, enc, dispatcher, broker)
	patroniHandler := handlers.NewPatroniHandler(clusterQ, nodeQ, credQ, taskQ, retentionQ, failoverEventQ, enc, dispatcher, witnessManager)
	metricsHandler := handlers.NewMetricsHandler(h, clusterQ, nodeQ)

	// Router
	e := api.New(api.RouterConfig{
		AuthHandler:       authHandler,
		AgentHandler:      agentHandler,
		ClusterHandler:    clusterHandler,
		NodeHandler:       nodeHandler,
		CredentialHandler: credHandler,
		DownloadHandler:   downloadHandler,
		ConfigHandler:     configHandler,
		PatroniHandler:    patroniHandler,
		StagingHandler:    stagingHandler,
		MetricsHandler:    metricsHandler,
		AlertHandler:      alertHandler,
		TaskResultQuerier: taskQ,
		SSEBroker:         broker,
		AuditQuerier:      auditQ,
		JWTSecret:         jwtSecret,
	})

	// HTTP server
	srv := &http.Server{
		Addr:         addr,
		Handler:      e,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if tlsCertFile != "" {
			slog.Info("server listening (HTTPS)", "addr", addr, "cert", tlsCertFile)
			if err := srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		} else {
			slog.Warn("server listening (HTTP — TLS disabled, not recommended for production)", "addr", addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down — draining agent connections")
		h.DrainAll() // send close frame to all connected agents before HTTP shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runMigrations(dsn string) error {
	migrateDSN := dsn
	if len(dsn) >= 11 && dsn[:11] == "postgres://" {
		migrateDSN = "pgx5://" + dsn[11:]
	} else if len(dsn) >= 14 && dsn[:14] == "postgresql://" {
		migrateDSN = "pgx5://" + dsn[14:]
	} else {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			return fmt.Errorf("parsing DSN for migrations: %w", err)
		}
		cc := cfg.ConnConfig
		migrateDSN = fmt.Sprintf("pgx5://%s:%s@%s:%d/%s?sslmode=disable",
			cc.User, cc.Password, cc.Host, cc.Port, cc.Database)
	}
	src, err := iofs.New(dbmigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateDSN)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

func seedAdminIfEmpty(ctx context.Context, userQ *queries.UserQuerier) error {
	users, err := userQ.List(ctx)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil
	}
	password := "changeme123!"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	user, err := userQ.Create(ctx, "admin", string(hash), "admin")
	if err != nil {
		return err
	}
	log.Printf("⚠  Created default admin user: username=admin password=%s  — CHANGE THIS IMMEDIATELY", password)
	log.Printf("   User ID: %s", user.ID)
	return nil
}

// recoverWitnesses restarts patroni_raft_controller witnesses for every
// active_standby cluster that already has Patroni configured. Called once
// at startup so witnesses survive conductor restarts without needing a
// manual configure_failover trigger.
func recoverWitnesses(ctx context.Context, wm *patroni.WitnessManager, clusterQ *queries.ClusterQuerier, nodeQ *queries.NodeQuerier) {
	clusters, err := clusterQ.List(ctx)
	if err != nil {
		log.Printf("witness recovery: failed to list clusters: %v", err)
		return
	}

	var infos []patroni.ClusterWitnessInfo
	for _, c := range clusters {
		if c.Mode != "active_standby" || !c.PatroniConfigured {
			continue
		}
		nodes, err := nodeQ.ListByCluster(ctx, c.ID)
		if err != nil {
			log.Printf("witness recovery: failed to list nodes for cluster %s: %v", c.ID, err)
			continue
		}
		var peers []string
		for _, n := range nodes {
			if n.Role == "hyperconverged" || n.Role == "db_only" {
				ip, _, _ := strings.Cut(n.IPAddress, "/")
				peers = append(peers, ip+":5433")
			}
		}
		if len(peers) > 0 {
			infos = append(infos, patroni.ClusterWitnessInfo{
				ClusterID:    c.ID,
				PartnerAddrs: peers,
			})
		}
	}

	if len(infos) > 0 {
		log.Printf("witness recovery: recovering %d witness(es)", len(infos))
		wm.RecoverAll(infos)
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
