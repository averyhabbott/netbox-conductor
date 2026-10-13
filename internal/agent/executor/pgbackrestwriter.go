package executor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const defaultPGBackRestConfigPath = "/etc/pgbackrest/pgbackrest.conf"

// WritePGBackRestConfig atomically writes /etc/pgbackrest/pgbackrest.conf.
// Mirrors WritePatroniConfig — sha256 integrity check, temp-file atomic rename,
// and explicit permission setting so the postgres OS user can read the file.
func WritePGBackRestConfig(params protocol.PGBackRestConfigParams) (string, error) {
	configPath := defaultPGBackRestConfigPath

	if _, err := checkSHA256(params.Config, params.Sha256); err != nil {
		return "", err
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating pgbackrest config dir: %w", err)
	}
	_ = os.Chmod(dir, 0755)

	// Mode 0640 with group=postgres lets postgres read without requiring root ownership.
	if err := writeFileAtomic(configPath, params.Config, 0640, groupID("postgres")); err != nil {
		return "", err
	}

	return fmt.Sprintf("wrote %d bytes to %s", len(params.Config), configPath), nil
}
