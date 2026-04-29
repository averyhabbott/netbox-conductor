package executor

import (
	"fmt"
	"os/exec"
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
	if err := waitForPromotion(postgresDataDir(), 2*time.Minute); err != nil {
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
// The conductor is responsible for stopping Patroni on all cluster members
// before dispatching this task. After this task completes successfully, the
// conductor reinitializes replica nodes.
//
// Sequence:
//  1. Stop Patroni (should already be stopped by conductor, but ensure it)
//  2. Run pgbackrest restore with the target time
//  3. Start PostgreSQL in recovery mode
//  4. Poll pg_is_in_recovery() until the instance promotes
//  5. Start Patroni to take over cluster management
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
		dataDir = postgresDataDir()
	}

	var log strings.Builder

	// Step 1: Ensure Patroni is stopped.
	if out, err := runSystemctl("stop", "patroni"); err != nil {
		// Log the error but continue — the conductor may have already stopped it.
		log.WriteString(fmt.Sprintf("patroni stop (may already be stopped): %v\n%s\n", err, out))
	} else {
		log.WriteString("patroni stopped\n")
	}

	// Step 2: Run pgBackRest restore.
	restoreCmd := params.RestoreCmd
	if restoreCmd == "" {
		restoreCmd = fmt.Sprintf(
			"pgbackrest --stanza=%s --type=time --target=%q --target-action=promote restore",
			params.Stanza, params.TargetTime,
		)
	}
	parts := strings.Fields(restoreCmd)
	if len(parts) == 0 {
		return log.String(), fmt.Errorf("empty restore command")
	}
	restoreOut, err := exec.Command(parts[0], parts[1:]...).CombinedOutput() //nolint:gosec
	if err != nil {
		return log.String() + string(restoreOut), fmt.Errorf("pgbackrest restore failed: %w", err)
	}
	log.WriteString(fmt.Sprintf("pgbackrest restore complete\n%s\n", restoreOut))

	// Step 3: Start PostgreSQL in recovery mode.
	pgctlOut, err := exec.Command("sudo", "-u", "postgres", "pg_ctl", "start", "-D", dataDir, "-w").CombinedOutput()
	if err != nil {
		return log.String() + string(pgctlOut), fmt.Errorf("postgres start failed: %w\n%s", err, pgctlOut)
	}
	log.WriteString(fmt.Sprintf("postgres started\n%s\n", pgctlOut))

	// Step 4: Poll until PostgreSQL has promoted out of recovery.
	if err := waitForPromotion(dataDir, 10*time.Minute); err != nil {
		return log.String(), fmt.Errorf("waiting for promotion: %w", err)
	}
	log.WriteString("postgres promoted (no longer in recovery)\n")

	// Step 5: Start Patroni to resume cluster management.
	if patroniOut, err := runSystemctl("start", "patroni"); err != nil {
		return log.String() + patroniOut, fmt.Errorf("patroni start failed: %w\n%s", err, patroniOut)
	}
	log.WriteString("patroni started\n")

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

// runSystemctl runs a systemctl command and returns output and error.
func runSystemctl(action, unit string) (string, error) {
	out, err := exec.Command("systemctl", action, unit).CombinedOutput()
	return string(out), err
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

// postgresDataDir attempts to detect the PostgreSQL data directory from
// pg_lsclusters (Debian/Ubuntu). Falls back to the standard path.
func postgresDataDir() string {
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

// pgSocketDir returns the Unix socket directory for a given data dir.
// PostgreSQL typically uses /var/run/postgresql for peer-auth connections.
func pgSocketDir(_ string) string {
	return "/var/run/postgresql"
}
