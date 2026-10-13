package executor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// WriteConfig atomically writes configuration.py and optionally restarts NetBox.
// Returns output text and any error.
func WriteConfig(ctx context.Context, params protocol.ConfigWriteParams, configPath string) (string, error) {
	if configPath == "" {
		return "", ErrNoNetbox
	}

	// Verify SHA-256 before touching disk
	actual, err := checkSHA256(params.Content, params.Sha256)
	if err != nil {
		return "", err
	}

	if err := writeNetboxConfig(ctx, configPath, params.Content, params.BackupExisting); err != nil {
		return "", err
	}

	output := fmt.Sprintf("wrote %d bytes to %s (sha256=%s)", len(params.Content), configPath, actual)
	return withNetboxRestart(ctx, output, params.RestartAfter), nil
}

// checkSHA256 returns content's hex SHA-256, or an error if want is set and
// doesn't match it.
func checkSHA256(content, want string) (string, error) {
	sum := sha256.Sum256([]byte(content))
	actual := fmt.Sprintf("%x", sum[:])
	if want != "" && actual != want {
		return "", fmt.Errorf("sha256 mismatch: expected %s got %s", want, actual)
	}
	return actual, nil
}

// writeFileAtomic writes content to a temp file in path's directory (required
// for an atomic rename on the same fs), sets its group (gid -1 leaves it alone)
// and mode, then renames it over path so readers never see a partial file.
// The temp file starts at 0600 so secrets aren't exposed before the chmod.
func writeFileAtomic(path, content string, mode os.FileMode, gid int) error {
	tmpPath := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), time.Now().UnixNano()))
	if err := os.WriteFile(tmpPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("writing temp file for %s: %w", path, err)
	}
	if gid >= 0 {
		_ = os.Chown(tmpPath, -1, gid) // best effort: the agent may not be in the group
	}
	// Explicit chmod bypasses any restrictive umask on the agent service.
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomic rename to %s failed: %w", path, err)
	}
	return nil
}

// groupID returns the numeric ID of the named OS group, or -1 if it can't be found.
func groupID(name string) int {
	g, err := user.LookupGroup(name)
	if err != nil {
		return -1
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1
	}
	return gid
}

// withNetboxRestart restarts NetBox when restart is set and appends the result
// to output. A failed restart is only a warning: the config is already on disk
// and the next systemd restart will pick it up.
func withNetboxRestart(ctx context.Context, output string, restart bool) string {
	if !restart {
		return output
	}
	restartOut, err := restartNetbox(ctx)
	output += "\n" + restartOut
	if err != nil {
		output += "\nwarn: restart failed: " + err.Error()
	}
	return output
}

// restartNetbox calls systemctl to restart both netbox and netbox-rq services.
func restartNetbox(ctx context.Context) (string, error) {
	cmd := Command(ctx, "sudo", "/usr/bin/systemctl", "restart", "netbox", "netbox-rq")
	if out, err := cmd.CombinedOutput(); err != nil {
		return string(out), fmt.Errorf("systemctl restart netbox netbox-rq: %w", err)
	}
	return "restarted netbox and netbox-rq", nil
}
