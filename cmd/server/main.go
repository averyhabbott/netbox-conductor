package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abottVU/netbox-failover/internal/server/api"
	"github.com/abottVU/netbox-failover/internal/server/api/handlers"
	"github.com/abottVU/netbox-failover/internal/server/crypto"
	"github.com/abottVU/netbox-failover/internal/server/db"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/abottVU/netbox-failover/internal/server/hub"
	"github.com/abottVU/netbox-failover/internal/server/patroni"
	"github.com/abottVU/netbox-failover/internal/server/sse"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	_ = godotenv.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func run(ctx context.Context) error {
	dsn := requireEnv("DATABASE_URL")
	jwtSecret := []byte(requireEnv("JWT_SECRET"))
	addr := envOr("LISTEN_ADDR", ":8080")
	migrationPath := envOr("MIGRATION_PATH", "file://internal/server/db/migrations")
	serverURL := envOr("SERVER_URL", "")    // base URL shown in agent ENV snippets
	serverBindIP := envOr("SERVER_BIND_IP", "") // IP for witness to listen on

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
	if err := runMigrations(dsn, migrationPath); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	log.Println("migrations applied")

	// Queriers
	userQ := queries.NewUserQuerier(store.Pool())
	refreshQ := queries.NewRefreshTokenQuerier(store.Pool())
	nodeQ := queries.NewNodeQuerier(store.Pool())
	agentTokQ := queries.NewAgentTokenQuerier(store.Pool())
	regTokQ := queries.NewRegistrationTokenQuerier(store.Pool())
	clusterQ := queries.NewClusterQuerier(store.Pool())
	credQ := queries.NewCredentialQuerier(store.Pool())
	auditQ := queries.NewAuditQuerier(store.Pool())
	configQ := queries.NewConfigQuerier(store.Pool())
	taskQ := queries.NewTaskResultQuerier(store.Pool())

	// Seed default admin
	if err := seedAdminIfEmpty(ctx, userQ); err != nil {
		return fmt.Errorf("seeding admin: %w", err)
	}

	// Hub + SSE broker
	h := hub.New()
	dispatcher := hub.NewDispatcher(h)
	broker := sse.New()

	// Patroni witness manager
	witnessManager := patroni.NewWitnessManager(patroni.WitnessConfig{
		ServerAddr: serverBindIP,
	})

	// Handlers
	authHandler := handlers.NewAuthHandler(userQ, refreshQ, jwtSecret)
	agentHandler := handlers.NewAgentHandler(h, dispatcher, broker, nodeQ, agentTokQ, regTokQ, taskQ, enc)
	clusterHandler := handlers.NewClusterHandler(clusterQ, nodeQ, regTokQ, h, enc)
	nodeHandler := handlers.NewNodeHandler(nodeQ, regTokQ, agentTokQ, taskQ, h, serverURL)
	credHandler := handlers.NewCredentialHandler(credQ, enc)
	configHandler := handlers.NewConfigHandler(configQ, taskQ, nodeQ, clusterQ, credQ, enc, dispatcher, broker)
	patroniHandler := handlers.NewPatroniHandler(clusterQ, nodeQ, credQ, taskQ, enc, dispatcher, witnessManager)

	// Router
	e := api.New(api.RouterConfig{
		AuthHandler:       authHandler,
		AgentHandler:      agentHandler,
		ClusterHandler:    clusterHandler,
		NodeHandler:       nodeHandler,
		CredentialHandler: credHandler,
		ConfigHandler:     configHandler,
		PatroniHandler:    patroniHandler,
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
		log.Printf("server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runMigrations(dsn, migrationPath string) error {
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
	m, err := migrate.New(migrationPath, migrateDSN)
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
