package executor

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const defaultPGBackRestConfigPath = "/etc/pgbackrest/pgbackrest.conf"

// WritePGBackRestConfig atomically writes /etc/pgbackrest/pgbackrest.conf.
// Mirrors WritePatroniConfig — sha256 integrity check, temp-file atomic rename,
// and explicit permission setting so the postgres OS user can read the file.
func WritePGBackRestConfig(params protocol.PGBackRestConfigParams) (string, error) {
	configPath := defaultPGBackRestConfigPath

	if params.Sha256 != "" {
		sum := sha256.Sum256([]byte(params.Config))
		if actual := fmt.Sprintf("%x", sum[:]); actual != params.Sha256 {
			return "", fmt.Errorf("sha256 mismatch: expected %s got %s", params.Sha256, actual)
		}
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating pgbackrest config dir: %w", err)
	}
	_ = os.Chmod(dir, 0755)

	tmpPath := filepath.Join(dir, fmt.Sprintf(".pgbackrest.conf.tmp.%d", time.Now().UnixNano()))
	if err := os.WriteFile(tmpPath, []byte(params.Config), 0640); err != nil {
		return "", fmt.Errorf("writing temp pgbackrest config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("atomic rename of pgbackrest config failed: %w", err)
	}
	// Mode 0640 with group=postgres lets postgres read without requiring root ownership.
	_ = os.Chmod(configPath, 0640)
	if pg, err := user.LookupGroup("postgres"); err == nil {
		if gid, err := strconv.Atoi(pg.Gid); err == nil {
			_ = os.Chown(configPath, -1, gid)
		}
	}

	return fmt.Sprintf("wrote %d bytes to %s", len(params.Config), configPath), nil
}
