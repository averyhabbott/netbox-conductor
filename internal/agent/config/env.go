// Package config loads and validates the agent's ENV file configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/env"
	"github.com/joho/godotenv"
)

// Config holds all agent configuration loaded from the ENV file.
type Config struct {
	// Identity (set after registration)
	NodeID string
	Token  string

	// Server connection
	ServerURL     string
	TLSSkipVerify bool
	TLSCACert     string

	// Logging
	LogLevel string

	// NetBox paths, derived from NETBOX_ROOT. Empty when the node has no NetBox
	// (db_only nodes; install.sh --db-only writes NETBOX_ROOT empty).
	NetboxConfigPath string
	NetboxMediaRoot  string
	NetboxLogPath    string

	// Patroni
	PatroniRESTURL string

	// Status server
	// Local HTTP server that returns 200/503 based on whether netbox.service is
	// active. Binds to loopback by default; accessed through the node's nginx/Apache
	// reverse proxy which exposes /status on the public HTTPS port.
	// Empty string disables the server.
	// Env var: AGENT_STATUS_ADDR (preferred) or legacy AGENT_STATUS_PORT (int).
	StatusAddr string
}

// Load reads configuration from the ENV file at path (if set) plus the
// process environment. Process environment takes precedence.
func Load(envFile string) (*Config, error) {
	// Load the env file if it exists; ignore "file not found"
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("loading env file %s: %w", envFile, err)
		}
	}

	cfg := &Config{
		NodeID:         os.Getenv("AGENT_NODE_ID"),
		Token:          os.Getenv("AGENT_TOKEN"),
		ServerURL:      os.Getenv("AGENT_SERVER_URL"),
		TLSCACert:      os.Getenv("AGENT_TLS_CA_CERT"),
		LogLevel:       env.Or("AGENT_LOG_LEVEL", "info"),
		NetboxLogPath:  os.Getenv("NETBOX_LOG_PATH"),
		PatroniRESTURL: env.Or("PATRONI_REST_URL", "http://127.0.0.1:8008"),
		StatusAddr:     envStatusAddr(),
	}

	skipVerify := strings.ToLower(os.Getenv("AGENT_TLS_SKIP_VERIFY"))
	cfg.TLSSkipVerify = skipVerify == "true" || skipVerify == "1" || skipVerify == "yes"

	if root := os.Getenv("NETBOX_ROOT"); root != "" {
		cfg.NetboxConfigPath = filepath.Join(root, "netbox/netbox/configuration.py")
		cfg.NetboxMediaRoot = filepath.Join(root, "netbox/media")
	}

	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("AGENT_SERVER_URL is required")
	}
	// Accept https:// and http:// by converting to the WebSocket equivalents.
	c.ServerURL = strings.NewReplacer(
		"https://", "wss://",
		"http://", "ws://",
	).Replace(c.ServerURL)
	if !strings.HasPrefix(c.ServerURL, "wss://") && !strings.HasPrefix(c.ServerURL, "ws://") {
		return fmt.Errorf("AGENT_SERVER_URL must start with https://, wss://, http://, or ws://")
	}
	if strings.HasPrefix(c.ServerURL, "ws://") && !c.TLSSkipVerify {
		return fmt.Errorf("unencrypted ws:// requires AGENT_TLS_SKIP_VERIFY=true (development only)")
	}
	if c.TLSSkipVerify {
		fmt.Fprintln(os.Stderr, "WARNING: AGENT_TLS_SKIP_VERIFY=true — TLS certificate verification is disabled. Do not use in production.")
	}
	return nil
}

// IsRegistered reports whether the agent has a NodeID and Token set.
func (c *Config) IsRegistered() bool {
	return c.NodeID != "" && c.Token != ""
}

// envStatusAddr resolves the status server bind address.
// Checks AGENT_STATUS_ADDR first (new), then falls back to AGENT_STATUS_PORT
// (legacy int) for backward compatibility. Default: 127.0.0.1:8081.
func envStatusAddr() string {
	if v := os.Getenv("AGENT_STATUS_ADDR"); v != "" {
		return v
	}
	if port := env.Int("AGENT_STATUS_PORT", 0); port > 0 {
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	return "127.0.0.1:8081"
}
