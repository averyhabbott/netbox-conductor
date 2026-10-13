package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// LearnedCertPath is where the agent saves the conductor's CA cert. Its folder
// is the only part of /etc/netbox-agent the agent can write (install.sh).
const LearnedCertPath = "/etc/netbox-agent/certs/ca.crt"

// NeedsCert reports whether the agent should learn the conductor's CA cert:
// AGENT_TLS_CA_CERT points at LearnedCertPath and the file isn't there yet.
// Deleting the file makes the agent learn it again on the next start.
func NeedsCert(cfg *Config) bool {
	if cfg.TLSCACert != LearnedCertPath {
		return false
	}
	_, err := os.Stat(LearnedCertPath)
	return os.IsNotExist(err)
}

// LearnCert downloads the conductor's CA certificate (using an insecure client)
// once and saves it to LearnedCertPath, which later connections pin.
func LearnCert(cfg *Config) error {
	if cfg.ServerURL == "" {
		return fmt.Errorf("AGENT_SERVER_URL is not set")
	}

	certURL := cfg.BaseURL() + "/api/v1/downloads/ca.crt"

	// Use an insecure client for the one-time download.
	insecure := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec — intentional one-time cert fetch
		},
	}

	resp, err := insecure.Get(certURL) //nolint:noctx
	if err != nil {
		return fmt.Errorf("downloading CA cert from %s: %w", certURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CA cert download returned %d", resp.StatusCode)
	}

	certData, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("reading CA cert: %w", err)
	}
	if err := checkCertPEM(certData); err != nil {
		return fmt.Errorf("CA cert from %s: %w", certURL, err)
	}

	if err := os.WriteFile(LearnedCertPath, certData, 0640); err != nil {
		return fmt.Errorf("writing CA cert to %s: %w", LearnedCertPath, err)
	}
	return nil
}

// checkCertPEM confirms data is a PEM certificate, so an error page or a proxy's
// login page is never saved and pinned as the conductor's CA.
func checkCertPEM(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("response is not a PEM certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("parsing certificate: %w", err)
	}
	return nil
}
