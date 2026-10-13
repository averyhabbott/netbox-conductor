package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const defaultSentinelConfigPath = "/etc/redis/sentinel.conf"

// WriteSentinelConfig atomically writes sentinel.conf and optionally restarts redis-sentinel.
func WriteSentinelConfig(ctx context.Context, params protocol.SentinelConfigWriteParams) (string, error) {
	configPath := defaultSentinelConfigPath

	if _, err := checkSHA256(params.Content, params.Sha256); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}
	if err := writeFileAtomic(configPath, params.Content, 0660, -1); err != nil {
		return "", err
	}

	output := fmt.Sprintf("wrote %d bytes to %s", len(params.Content), configPath)

	if params.RestartAfter {
		cmd := Command(ctx, "sudo", "systemctl", "restart", "redis-sentinel")
		if out, err := cmd.CombinedOutput(); err != nil {
			return output, fmt.Errorf("redis-sentinel restart failed: %w\n%s", err, out)
		}
		output += "\nrestarted redis-sentinel"
	}

	return output, nil
}
