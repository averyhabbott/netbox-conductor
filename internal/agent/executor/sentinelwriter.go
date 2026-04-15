package executor

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const defaultSentinelConfigPath = "/etc/redis/sentinel.conf"

// WriteSentinelConfig atomically writes sentinel.conf and optionally restarts redis-sentinel.
func WriteSentinelConfig(params protocol.SentinelConfigWriteParams) (string, error) {
	configPath := defaultSentinelConfigPath

	if params.Sha256 != "" {
		sum := sha256.Sum256([]byte(params.Content))
		if actual := fmt.Sprintf("%x", sum[:]); actual != params.Sha256 {
			return "", fmt.Errorf("sha256 mismatch: expected %s got %s", params.Sha256, actual)
		}
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}

	tmpPath := filepath.Join(dir, fmt.Sprintf(".sentinel-config.tmp.%d", time.Now().UnixNano()))
	if err := os.WriteFile(tmpPath, []byte(params.Content), 0640); err != nil {
		return "", fmt.Errorf("writing temp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("atomic rename failed: %w", err)
	}

	output := fmt.Sprintf("wrote %d bytes to %s", len(params.Content), configPath)

	if params.RestartAfter {
		cmd := exec.Command("systemctl", "restart", "redis-sentinel")
		if out, err := cmd.CombinedOutput(); err != nil {
			output += "\nwarn: redis-sentinel restart failed: " + err.Error() + "\n" + string(out)
		} else {
			output += "\nrestarted redis-sentinel"
		}
	}

	return output, nil
}
