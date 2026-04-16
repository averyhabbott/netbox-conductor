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

const defaultPatroniConfigPath = "/etc/patroni/patroni.yml"

// WritePatroniConfig atomically writes patroni.yml and optionally restarts Patroni.
func WritePatroniConfig(params protocol.PatroniConfigWriteParams) (string, error) {
	configPath := defaultPatroniConfigPath

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

	tmpPath := filepath.Join(dir, fmt.Sprintf(".patroni-config.tmp.%d", time.Now().UnixNano()))
	// 0644 so the postgres OS user (which runs patronictl) can read the file
	// without needing to be in the netbox-agent group.
	if err := os.WriteFile(tmpPath, []byte(params.Content), 0644); err != nil {
		return "", fmt.Errorf("writing temp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("atomic rename failed: %w", err)
	}

	// The Debian/Ubuntu Patroni systemd unit ships with
	//   ConditionPathExists=/etc/patroni/config.yml
	// but the conductor writes to patroni.yml. Create a symlink so systemd
	// will actually start the service. This is idempotent — Remove ignores
	// "no such file" errors.
	symlinkPath := filepath.Join(dir, "config.yml")
	_ = os.Remove(symlinkPath) // remove stale file or old symlink if present
	symlinkNote := ", symlinked config.yml"
	if err := os.Symlink("patroni.yml", symlinkPath); err != nil {
		symlinkNote = fmt.Sprintf(", warn: config.yml symlink failed: %v", err)
	}

	output := fmt.Sprintf("wrote %d bytes to %s%s", len(params.Content), configPath, symlinkNote)

	if params.RestartAfter {
		cmd := exec.Command("systemctl", "restart", "patroni")
		if out, err := cmd.CombinedOutput(); err != nil {
			output += "\nwarn: patroni restart failed: " + err.Error() + "\n" + string(out)
		} else {
			output += "\nrestarted patroni"
		}
	}

	return output, nil
}

// RunCommand executes an arbitrary command (admin-only exec.run task).
func RunCommand(params protocol.RunCommandParams) (string, error) {
	if params.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	cmd := exec.Command(params.Command, params.Args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("command failed: %w", err)
	}
	return string(out), nil
}
