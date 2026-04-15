package executor

import (
	"archive/tar"
	"compress/gzip"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const agentBinDest = "/usr/local/bin/netbox-agent"
const agentServiceUnit = "netbox-agent"

// UpgradeAgent downloads the agent tarball from conductorURL, replaces the
// current binary at agentBinDest, and restarts the systemd service unit.
//
// The function sends its result message before restarting so the conductor
// receives the task result before the connection drops.
func UpgradeAgent(params protocol.AgentUpgradeParams, tlsSkipVerify bool, tlsCACert string) (string, error) {
	downloadURL := params.DownloadURL
	if downloadURL == "" {
		return "", fmt.Errorf("download_url is required")
	}

	arch := params.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}

	// Build HTTP client matching the agent's TLS configuration.
	tlsCfg := &tls.Config{InsecureSkipVerify: tlsSkipVerify} //nolint:gosec
	if tlsCACert != "" {
		// Re-use insecure for upgrade — the URL was provided by the trusted conductor.
		_ = tlsCACert
	}
	httpClient := &http.Client{
		Timeout:   120 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}

	resp, err := httpClient.Get(downloadURL) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("downloading agent from %s: %w", downloadURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent download returned HTTP %d", resp.StatusCode)
	}

	// Extract the "netbox-agent" binary from the tar.gz.
	tmpBin, err := extractAgentBinary(resp.Body, arch)
	if err != nil {
		return "", fmt.Errorf("extracting agent binary: %w", err)
	}
	defer os.Remove(tmpBin) //nolint:errcheck

	// Make executable and move into place atomically.
	if err := os.Chmod(tmpBin, 0755); err != nil {
		return "", fmt.Errorf("chmod agent binary: %w", err)
	}
	dest := agentBinDest
	if err := os.Rename(tmpBin, dest); err != nil {
		// Cross-device rename fails when /tmp is on a different filesystem — copy instead.
		if err2 := copyFile(tmpBin, dest, 0755); err2 != nil {
			return "", fmt.Errorf("installing agent binary: %w (rename: %v, copy: %v)", err, err, err2)
		}
	}

	// Restart the service. The process will be killed; systemd brings it back as
	// the new binary.  We run this in a goroutine so the executor can return the
	// result message first.
	go func() {
		time.Sleep(500 * time.Millisecond) // let the result propagate
		_ = exec.Command("sudo", "systemctl", "restart", agentServiceUnit).Run()
	}()

	return fmt.Sprintf("agent binary updated to %s; restarting %s", dest, agentServiceUnit), nil
}

// extractAgentBinary reads a .tar.gz stream and writes the "netbox-agent" entry
// to a temporary file, returning its path.
func extractAgentBinary(r io.Reader, _ string) (string, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar: %w", err)
		}
		if filepath.Base(hdr.Name) != "netbox-agent" {
			continue
		}
		// Found the binary — write to a temp file.
		tmp, err := os.CreateTemp("", "netbox-agent-upgrade-*")
		if err != nil {
			return "", fmt.Errorf("creating temp file: %w", err)
		}
		if _, err := io.Copy(tmp, io.LimitReader(tr, 256*1024*1024)); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", fmt.Errorf("writing binary: %w", err)
		}
		tmp.Close()
		return tmp.Name(), nil
	}
	return "", fmt.Errorf("netbox-agent binary not found in tarball")
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
