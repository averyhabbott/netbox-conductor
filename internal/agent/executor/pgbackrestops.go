package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// RunPGBackRestStanzaCreate runs stanza-create followed by stanza-check.
// This is a one-time bootstrap step that must complete before any backups run.
// The conductor calls this after pushing pgbackrest.conf for the first time.
func RunPGBackRestStanzaCreate(params protocol.PGBackRestStanzaCreateParams) (string, error) {
	stanza := params.Stanza
	if stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}

	// Wait for PostgreSQL to be primary before running stanza-create.
	// After a Patroni restart, PostgreSQL starts briefly in recovery mode
	// before promoting. pg_isready passes during that window, but pgbackrest
	// stanza-create requires a primary and will fail with "all clusters in
	// recovery" if it runs too early.
	if err := waitForPromotion(PostgresDataDir(), 2*time.Minute); err != nil {
		return "", fmt.Errorf("PostgreSQL not ready as primary: %w", err)
	}

	createOut, err := pgbackrest(stanza, "stanza-create")
	if err != nil {
		return createOut, fmt.Errorf("stanza-create failed: %w\n%s", err, createOut)
	}

	checkOut, err := pgbackrest(stanza, "check")
	if err != nil {
		return createOut + "\n" + checkOut, fmt.Errorf("stanza-check failed: %w\n%s", err, checkOut)
	}

	return fmt.Sprintf("stanza-create: ok\nstanza-check: ok\n%s", checkOut), nil
}

// RunPGBackRestBackup runs a pgBackRest backup of the given type (full|diff|incr).
func RunPGBackRestBackup(params protocol.PGBackRestBackupParams) (string, error) {
	stanza := params.Stanza
	if stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}
	backupType := params.Type
	if backupType == "" {
		backupType = "incr"
	}
	if backupType != "full" && backupType != "diff" && backupType != "incr" {
		return "", fmt.Errorf("invalid backup type %q (must be full|diff|incr)", backupType)
	}

	out, err := pgbackrest(stanza, "backup", "--type="+backupType)
	if err != nil {
		return out, fmt.Errorf("pgbackrest backup (%s) failed: %w\n%s", backupType, err, out)
	}
	return fmt.Sprintf("backup type=%s complete\n%s", backupType, out), nil
}

// RunPGBackRestCatalog returns the raw JSON output of `pgbackrest info`.
// The conductor parses this to populate the backup catalog cache and
// determine the oldest/newest available restore points for the UI.
func RunPGBackRestCatalog(params protocol.PGBackRestCatalogParams) (string, error) {
	stanza := params.Stanza
	if stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}

	out, err := pgbackrest(stanza, "info", "--output=json")
	if err != nil {
		return out, fmt.Errorf("pgbackrest info failed: %w\n%s", err, out)
	}
	return out, nil
}

// RunPGBackRestRestore orchestrates a full point-in-time restore on this node.
// The conductor pauses Patroni automation (patronictl pause) and stops PostgreSQL
// on replica nodes before dispatching this task. After this task completes
// successfully, the conductor resumes Patroni and reinitializes replicas.
//
// Sequence:
//  1. Stop PostgreSQL locally (Patroni is paused — it will not restart it)
//  2. Run pgbackrest restore with the target time (as postgres user, with --delta)
//  3. Start PostgreSQL in recovery mode (distro-portable)
//  4. Poll pg_is_in_recovery() until the instance promotes
func RunPGBackRestRestore(params protocol.PGBackRestRestoreParams) (string, error) {
	if params.Stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}
	if params.TargetTime == "" {
		return "", fmt.Errorf("target_time is required")
	}
	if _, err := time.Parse(time.RFC3339, params.TargetTime); err != nil {
		return "", fmt.Errorf("invalid target_time %q (must be RFC3339): %w", params.TargetTime, err)
	}

	dataDir := params.DataDir
	if dataDir == "" {
		dataDir = PostgresDataDir()
	}

	var log strings.Builder

	// Step 1: Stop PostgreSQL — Patroni is paused so it will not restart it.
	if stopOut, err := stopPostgresql(dataDir); err != nil {
		// Log and continue — may already be stopped.
		log.WriteString(fmt.Sprintf("postgres stop (may already be stopped): %v\n%s\n", err, stopOut))
	} else {
		log.WriteString("postgres stopped\n")
	}

	// Step 2: Run pgBackRest restore as the postgres OS user.
	// pgbackrest requires YYYY-MM-DD HH:MM:SS+00, not RFC3339 (T/Z).
	pgbackrestTime := strings.NewReplacer("T", " ", "Z", "+00").Replace(params.TargetTime)

	var restoreArgs []string
	if params.RestoreCmd != "" {
		restoreArgs = strings.Fields(params.RestoreCmd) //nolint:gosec — operator-supplied command
	} else {
		restoreArgs = []string{
			"pgbackrest",
			"--stanza=" + params.Stanza,
			"--type=time",
			"--target=" + pgbackrestTime,
			"--target-action=promote",
			"--delta",
			"restore",
		}
	}
	if len(restoreArgs) == 0 {
		return log.String(), fmt.Errorf("empty restore command")
	}
	sudoArgs := append([]string{"-u", "postgres"}, restoreArgs...)
	restoreOut, err := exec.Command("sudo", sudoArgs...).CombinedOutput() //nolint:gosec
	if err != nil {
		return log.String() + string(restoreOut), fmt.Errorf("pgbackrest restore failed: %w", err)
	}
	log.WriteString(fmt.Sprintf("pgbackrest restore complete\n%s\n", restoreOut))

	// Step 3: Start PostgreSQL in recovery mode.
	pgctlOut, err := startPostgresql(dataDir)
	if err != nil {
		return log.String() + pgctlOut, fmt.Errorf("postgres start failed: %w\n%s", err, pgctlOut)
	}
	log.WriteString(fmt.Sprintf("postgres started\n%s\n", pgctlOut))

	// Step 4: Poll until PostgreSQL has promoted out of recovery.
	if err := waitForPromotion(dataDir, 10*time.Minute); err != nil {
		return log.String(), fmt.Errorf("waiting for promotion: %w", err)
	}
	log.WriteString("postgres promoted (no longer in recovery)\n")

	// Patroni resume is dispatched by the conductor after this task succeeds.
	return log.String(), nil
}

// RunPGBackRestTestPath verifies that the given path is writable by the postgres
// OS user, which is the user pgBackRest uses for posix repo operations.
// Uses psql COPY (already in sudoers) rather than /bin/sh, which sudo-rs blocks.
func RunPGBackRestTestPath(params protocol.PGBackRestTestPathParams) (string, error) {
	if params.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.Contains(params.Path, "'") {
		return "", fmt.Errorf("path contains invalid character: single quote")
	}
	testFile := params.Path + "/.conductor_pgbackrest_test"
	out, err := exec.Command("sudo", "-u", "postgres",
		"/usr/bin/psql", "-c",
		fmt.Sprintf("COPY (SELECT 1) TO '%s'", testFile),
	).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("path not writable by postgres: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("path %s is writable", params.Path), nil
}

// pgbackrest runs the pgbackrest binary as the postgres OS user with the given
// stanza and additional arguments. Returns combined stdout+stderr.
func pgbackrest(stanza string, subcommand string, extraArgs ...string) (string, error) {
	args := append([]string{"-u", "postgres", "pgbackrest", "--stanza=" + stanza, subcommand}, extraArgs...)
	out, err := exec.Command("sudo", args...).CombinedOutput()
	return string(out), err
}

// stopPostgresql stops the PostgreSQL process via pg_ctl (cross-distro).
// postmaster.pid lives in the data dir regardless of which process started PostgreSQL,
// so this works whether Patroni or systemd started the server.
func stopPostgresql(dataDir string) (string, error) {
	out, err := exec.Command("sudo", "-u", "postgres",
		"pg_ctl", "stop", "-D", dataDir, "-m", "fast", "-w").CombinedOutput()
	return string(out), err
}

// startPostgresql starts PostgreSQL in a distro-portable way.
// On RHEL/Arch, postgresql.conf lives inside the data dir — plain pg_ctl works.
// On Debian/Ubuntu, it lives in /etc/postgresql/<ver>/<cluster>/ and must be
// passed via -o "-c config_file=...".
func startPostgresql(dataDir string) (string, error) {
	args := []string{"-u", "postgres", "pg_ctl", "start", "-D", dataDir, "-w", "-t", "120"}
	if _, err := os.Stat(filepath.Join(dataDir, "postgresql.conf")); os.IsNotExist(err) {
		if confFile := findDebianConfFile(dataDir); confFile != "" {
			args = append(args, "-o", "-c config_file="+confFile)
		}
	}
	out, err := exec.Command("sudo", args...).CombinedOutput()
	return string(out), err
}

// findDebianConfFile uses pg_lsclusters to locate postgresql.conf for a given
// data directory. Returns "" on non-Debian systems or if not found.
func findDebianConfFile(dataDir string) string {
	out, err := exec.Command("pg_lsclusters", "-h").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && fields[5] == dataDir {
			return fmt.Sprintf("/etc/postgresql/%s/%s/postgresql.conf", fields[0], fields[1])
		}
	}
	return ""
}

// waitForPromotion polls pg_is_in_recovery() until it returns false,
// meaning PostgreSQL has completed recovery and promoted to primary.
// Uses sudo -u postgres psql so peer authentication works regardless of
// which OS user the agent service runs as.
func waitForPromotion(_ string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command(
			"sudo", "-u", "postgres", "psql", "-Atc", "SELECT pg_is_in_recovery()", "postgres",
		).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "f" {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out after %s waiting for PostgreSQL to promote", timeout)
}

// PostgresDataDir attempts to detect the PostgreSQL data directory from
// pg_lsclusters (Debian/Ubuntu). Falls back to the standard path.
func PostgresDataDir() string {
	out, err := exec.Command("pg_lsclusters", "-h").Output()
	if err != nil {
		return "/var/lib/postgresql/data"
	}
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) == 0 {
		return "/var/lib/postgresql/data"
	}
	fields := strings.Fields(lines[0])
	if len(fields) >= 6 {
		return fields[5]
	}
	return "/var/lib/postgresql/data"
}
