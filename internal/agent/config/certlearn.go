package config

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultCertPath = "/etc/netbox-agent/ca.crt"

// LearnCert downloads the conductor's CA certificate (using an insecure client),
// saves it to defaultCertPath, and updates the env file so that future runs use
// certificate pinning instead of TLS skip-verify.
//
// After a successful run the env file will contain:
//
//	AGENT_TLS_CA_CERT=/etc/netbox-agent/ca.crt
//	AGENT_TLS_SKIP_VERIFY=false
//	UPDATE_CERT=false
//
// The in-memory cfg fields are updated in-place so the caller can use them
// immediately without re-loading the file.
func LearnCert(cfg *Config, envFile string) error {
	if cfg.ServerURL == "" {
		return fmt.Errorf("AGENT_SERVER_URL is not set")
	}

	// Convert wss:// → https:// to build the download URL.
	httpsBase := strings.NewReplacer(
		"wss://", "https://",
		"ws://",  "http://",
	).Replace(cfg.ServerURL)

	// Strip the path suffix added by validate() — we want just the base URL.
	// ServerURL at this point may already have /api/v1/agent/connect appended.
	if idx := strings.Index(httpsBase, "/api/"); idx != -1 {
		httpsBase = httpsBase[:idx]
	}

	certURL := httpsBase + "/api/v1/downloads/ca.crt"

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
	if len(certData) == 0 {
		return fmt.Errorf("CA cert response was empty")
	}

	// Write cert file.
	if err := os.MkdirAll("/etc/netbox-agent", 0750); err != nil {
		return fmt.Errorf("creating /etc/netbox-agent: %w", err)
	}
	if err := os.WriteFile(defaultCertPath, certData, 0640); err != nil {
		return fmt.Errorf("writing CA cert to %s: %w", defaultCertPath, err)
	}

	// Update the env file in-place, preserving all other lines.
	if envFile != "" {
		if err := updateEnvFile(envFile, map[string]string{
			"AGENT_TLS_CA_CERT":   defaultCertPath,
			"AGENT_TLS_SKIP_VERIFY": "false",
			"UPDATE_CERT":         "false",
		}); err != nil {
			return fmt.Errorf("updating env file: %w", err)
		}
	}

	// Update in-memory config.
	cfg.TLSCACert = defaultCertPath
	cfg.TLSSkipVerify = false
	cfg.UpdateCert = false

	return nil
}

// updateEnvFile reads envFile line-by-line, updates or appends the given keys,
// and writes the result back to the same path.  Comments and blank lines are
// preserved.
func updateEnvFile(path string, updates map[string]string) error {
	// Read existing content.
	f, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var lines []string
	touched := make(map[string]bool)

	if f != nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			// Check if this line sets one of the keys we want to update.
			for key, val := range updates {
				if isEnvLine(line, key) {
					line = key + "=" + val
					touched[key] = true
					break
				}
			}
			lines = append(lines, line)
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			return err
		}
	}

	// Append any keys that weren't already in the file.
	for key, val := range updates {
		if !touched[key] {
			lines = append(lines, key+"="+val)
		}
	}

	// Write back atomically via a temp file.
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Rename(tmp, path)
}

// isEnvLine returns true if line sets the given key (ignoring whitespace and
// handling quoted values).
func isEnvLine(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return false
	}
	prefix := key + "="
	return strings.HasPrefix(trimmed, prefix)
}
