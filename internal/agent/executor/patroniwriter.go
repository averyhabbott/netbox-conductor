package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const defaultPatroniConfigPath = "/etc/patroni/patroni.yml"

// WritePatroniConfig atomically writes patroni.yml and optionally restarts Patroni.
func WritePatroniConfig(ctx context.Context, params protocol.PatroniConfigWriteParams) (string, error) {
	configPath := defaultPatroniConfigPath

	if _, err := checkSHA256(params.Content, params.Sha256); err != nil {
		return "", err
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}

	// patroni.yml holds DB passwords, so only the postgres group (Patroni,
	// patronictl) may read it.
	pgGID := groupID("postgres")
	if pgGID < 0 {
		return "", fmt.Errorf("postgres group not found — install PostgreSQL, then rerun install.sh")
	}
	if err := os.Chown(dir, -1, pgGID); err != nil {
		return "", fmt.Errorf("giving %s to the postgres group: %w — rerun install.sh (it adds netbox-agent to that group)", dir, err)
	}
	// Explicit chmod bypasses any restrictive umask on the agent service.
	_ = os.Chmod(dir, 0750)

	if err := writeFileAtomic(configPath, params.Content, 0640, pgGID); err != nil {
		return "", err
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

	// On Debian/Ubuntu, PostgreSQL keeps its config files in /etc/postgresql/<ver>/main/
	// rather than in the data directory. Patroni requires postgresql.conf to be present
	// in the data dir (or config_dir) before it can start. Copy any missing conf files
	// from the Debian location into the data dir so the first Patroni start succeeds.
	if confNote := ensurePostgresConfsInDataDir(ctx); confNote != "" {
		symlinkNote += confNote
	}

	output := fmt.Sprintf("wrote %d bytes to %s%s", len(params.Content), configPath, symlinkNote)

	if params.RestartAfter {
		cmd := Command(ctx, "systemctl", "restart", "patroni")
		if out, err := cmd.CombinedOutput(); err != nil {
			return output, fmt.Errorf("patroni restart failed: %w\n%s", err, out)
		}
		output += "\nrestarted patroni"
	}

	return output, nil
}

// ensurePostgresConfsInDataDir copies postgresql.conf, pg_hba.conf, and pg_ident.conf
// from the Debian/Ubuntu /etc/postgresql/<ver>/main/ directory into the PostgreSQL
// data directory if they are not already present. Returns a short status note.
func ensurePostgresConfsInDataDir(ctx context.Context) string {
	// Find the first PostgreSQL cluster via pg_lsclusters
	out, err := Command(ctx, "pg_lsclusters", "-h").Output()
	if err != nil {
		return "" // pg_lsclusters not available (non-Debian) — nothing to do
	}

	// Parse first data line: "17  main  5432  online  postgres  /var/lib/postgresql/17/main ..."
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) == 0 {
		return ""
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 6 {
		return ""
	}
	version := fields[0] // e.g. "17"
	cluster := fields[1] // e.g. "main"
	dataDir := fields[5] // e.g. "/var/lib/postgresql/17/main"

	etcDir := fmt.Sprintf("/etc/postgresql/%s/%s", version, cluster)
	copied := 0
	for _, name := range []string{"postgresql.conf", "pg_hba.conf", "pg_ident.conf"} {
		dst := filepath.Join(dataDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // already there
		}
		src := filepath.Join(etcDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0640); err != nil {
			continue
		}
		// Ensure postgres can read it
		_ = Command(ctx, "chown", "postgres:postgres", dst).Run()
		copied++
	}
	if copied > 0 {
		return fmt.Sprintf(", copied %d conf file(s) from %s to %s", copied, etcDir, dataDir)
	}
	return ""
}

// RunCommand executes an arbitrary command (admin-only exec.run task).
func RunCommand(ctx context.Context, params protocol.RunCommandParams) (string, error) {
	if params.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	cmd := Command(ctx, params.Command, params.Args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("command failed: %w", err)
	}
	return string(out), nil
}
