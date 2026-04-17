// Package config loads and validates the agent's ENV file configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all agent configuration loaded from the ENV file.
type Config struct {
	// Identity (set after registration)
	NodeID string
	Token  string

	// Server connection
	ServerURL             string
	TLSSkipVerify         bool
	TLSCACert             string
	ReconnectIntervalSecs int

	// Cert auto-learning
	// When true the agent downloads the conductor's CA cert on startup,
	// saves it to /etc/netbox-agent/ca.crt, and updates the env file.
	UpdateCert bool

	// Logging
	LogLevel string

	// NetBox paths
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
		NodeID:                os.Getenv("AGENT_NODE_ID"),
		Token:                 os.Getenv("AGENT_TOKEN"),
		ServerURL:             os.Getenv("AGENT_SERVER_URL"),
		TLSCACert:             os.Getenv("AGENT_TLS_CA_CERT"),
		LogLevel:              envOr("AGENT_LOG_LEVEL", "info"),
		NetboxConfigPath:      envOr("NETBOX_CONFIG_PATH", "/opt/netbox/netbox/netbox/configuration.py"),
		NetboxMediaRoot:       envOr("NETBOX_MEDIA_ROOT", "/opt/netbox/netbox/media"),
		NetboxLogPath:         os.Getenv("NETBOX_LOG_PATH"),
		PatroniRESTURL:        envOr("PATRONI_REST_URL", "http://127.0.0.1:8008"),
		ReconnectIntervalSecs: envInt("AGENT_RECONNECT_INTERVAL_SECS", 10),
		StatusAddr:            envStatusAddr(),
	}

	skipVerify := strings.ToLower(os.Getenv("AGENT_TLS_SKIP_VERIFY"))
	cfg.TLSSkipVerify = skipVerify == "true" || skipVerify == "1" || skipVerify == "yes"

	updateCert := strings.ToLower(os.Getenv("UPDATE_CERT"))
	cfg.UpdateCert = updateCert == "true" || updateCert == "1" || updateCert == "yes"

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
	if port := envInt("AGENT_STATUS_PORT", 0); port > 0 {
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	return "127.0.0.1:8081"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
