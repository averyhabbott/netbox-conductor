package executor

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/abottVU/netbox-failover/internal/shared/protocol"
)

// WriteConfig atomically writes configuration.py and optionally restarts NetBox.
// Returns output text and any error.
func WriteConfig(params protocol.ConfigWriteParams, configPath string) (string, error) {
	if configPath == "" {
		return "", fmt.Errorf("NETBOX_CONFIG_PATH is not configured")
	}

	// Verify SHA-256 before touching disk
	sum := sha256.Sum256([]byte(params.Content))
	actual := fmt.Sprintf("%x", sum[:])
	if params.Sha256 != "" && actual != params.Sha256 {
		return "", fmt.Errorf("sha256 mismatch: expected %s got %s", params.Sha256, actual)
	}

	dir := filepath.Dir(configPath)
	tmpPath := filepath.Join(dir, fmt.Sprintf(".netbox-agent-config.tmp.%d", time.Now().UnixNano()))

	// Write to temp file in same directory (required for atomic rename on same fs)
	if params.BackupExisting {
		if _, err := os.Stat(configPath); err == nil {
			backupPath := configPath + ".bak"
			_ = os.Rename(configPath, backupPath)
		}
	}

	if err := os.WriteFile(tmpPath, []byte(params.Content), 0640); err != nil {
		return "", fmt.Errorf("writing temp config: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("atomic rename failed: %w", err)
	}

	output := fmt.Sprintf("wrote %d bytes to %s (sha256=%s)", len(params.Content), configPath, actual)

	if params.RestartAfter {
		restartOut, err := restartNetbox()
		output += "\n" + restartOut
		if err != nil {
			// Non-fatal: config was written; log but don't fail the task
			output += "\nwarn: restart failed: " + err.Error()
		}
	}

	return output, nil
}

// restartNetbox calls systemctl to restart both netbox and netbox-rq services.
func restartNetbox() (string, error) {
	for _, svc := range []string{"netbox", "netbox-rq"} {
		cmd := exec.Command("systemctl", "restart", svc)
		if out, err := cmd.CombinedOutput(); err != nil {
			return string(out), fmt.Errorf("systemctl restart %s: %w", svc, err)
		}
	}
	return "restarted netbox and netbox-rq", nil
}
