package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// BaseURL returns the conductor's HTTP base URL: ServerURL with wss:// → https://
// (ws:// → http://) and the WebSocket path suffix (/api/...) stripped.
func (c *Config) BaseURL() string {
	base := strings.NewReplacer("wss://", "https://", "ws://", "http://").Replace(c.ServerURL)
	if idx := strings.Index(base, "/api/"); idx != -1 {
		base = base[:idx]
	}
	return base
}

// HTTPClient returns a client for talking to the conductor that trusts
// AGENT_TLS_CA_CERT (when set) and honours AGENT_TLS_SKIP_VERIFY.
func (c *Config) HTTPClient() (*http.Client, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: c.TLSSkipVerify, //nolint:gosec // intentional dev option, warned at startup
	}
	if c.TLSCACert != "" {
		pemData, err := os.ReadFile(c.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("reading CA cert %s: %w", c.TLSCACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("no valid certificates in %s", c.TLSCACert)
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}, nil
}
