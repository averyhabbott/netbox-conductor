package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// RunPGBackRestStanzaCreate runs stanza-create followed by stanza-check.
// This is a one-time bootstrap step that must complete before any backups run.
// The conductor calls this after pushing pgbackrest.conf for the first time.
func RunPGBackRestStanzaCreate(ctx context.Context, params protocol.PGBackRestStanzaCreateParams) (string, error) {
	stanza := params.Stanza
	if stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}

	// Wait for PostgreSQL to be primary before running stanza-create.
	// After a Patroni restart, PostgreSQL starts briefly in recovery mode
	// before promoting. pg_isready passes during that window, but pgbackrest
	// stanza-create requires a primary and will fail with "all clusters in
	// recovery" if it runs too early.
	if err := waitForPromotion(ctx, 2*time.Minute); err != nil {
		return "", fmt.Errorf("PostgreSQL not ready as primary: %w", err)
	}

	createOut, err := pgbackrest(ctx, stanza, "stanza-create")
	if err != nil {
		return createOut, fmt.Errorf("stanza-create failed: %w\n%s", err, createOut)
	}

	checkOut, err := pgbackrest(ctx, stanza, "check")
	if err != nil {
		return createOut + "\n" + checkOut, fmt.Errorf("stanza-check failed: %w\n%s", err, checkOut)
	}

	return fmt.Sprintf("stanza-create: ok\nstanza-check: ok\n%s", checkOut), nil
}

// RunPGBackRestBackups runs one backup round: each location (repo) in turn is
// checked, then backed up once. A failed check sends the location to the back
// of the queue, not ready again for CheckRetrySecs; the next ready location
// runs, or the round waits for the first to be ready. After CheckTries failed
// checks, or a failed backup, the location has failed. Warnings and errors go
// to report as they happen. The output lists every location's outcome.
func RunPGBackRestBackups(ctx context.Context, p protocol.PGBackRestBackupParams, report func(protocol.BackupEventPayload)) (string, error) {
	if p.Stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}
	if p.Type != "full" && p.Type != "diff" && p.Type != "incr" {
		return "", fmt.Errorf("invalid backup type %q (must be full|diff|incr)", p.Type)
	}
	for _, r := range p.Repos {
		if r < 1 || r > 4 {
			return "", fmt.Errorf("invalid repo %d (must be 1-4)", r)
		}
	}
	check := func(ctx context.Context, repo int) error {
		return checkRepo(ctx, p.Stanza, repo, time.Duration(p.CheckTimeoutSecs)*time.Second)
	}
	backup := func(ctx context.Context, repo int) error {
		ctx, cancel := context.WithTimeout(ctx, time.Duration(p.BackupTimeoutSecs)*time.Second)
		defer cancel()
		out, err := pgbackrest(ctx, p.Stanza, "backup", "--type="+p.Type, fmt.Sprintf("--repo=%d", repo))
		if err != nil {
			return fmt.Errorf("pgbackrest backup (%s) failed: %w: %s", p.Type, err, strings.TrimSpace(out))
		}
		return nil
	}
	return backupRound(ctx, p, check, backup, report)
}

// backupRound is RunPGBackRestBackups' queue, with the check and backup passed in.
func backupRound(ctx context.Context, p protocol.PGBackRestBackupParams, check, backup func(context.Context, int) error,
	report func(protocol.BackupEventPayload)) (string, error) {
	type loc struct {
		repo, fails int
		ready       time.Time
	}
	var queue []*loc
	for _, r := range p.Repos {
		queue = append(queue, &loc{repo: r})
	}
	var results []protocol.PGBackRestBackupResult
	failed := 0
	done := func(l *loc, err error) {
		res := protocol.PGBackRestBackupResult{Repo: l.repo}
		if err != nil {
			res.Error = err.Error()
			failed++
			report(protocol.BackupEventPayload{Repo: l.repo, Error: true, Message: res.Error})
		}
		results = append(results, res)
	}
	for len(queue) > 0 {
		i := slices.IndexFunc(queue, func(l *loc) bool { return !time.Now().Before(l.ready) })
		if i < 0 {
			first := slices.MinFunc(queue, func(a, b *loc) int { return a.ready.Compare(b.ready) })
			if err := sleep(ctx, time.Until(first.ready)); err != nil {
				for _, l := range queue {
					done(l, fmt.Errorf("round ended before its backup: %w", err))
				}
				break
			}
			continue
		}
		l := queue[i]
		queue = slices.Delete(queue, i, i+1)
		if err := check(ctx, l.repo); err != nil {
			if l.fails++; l.fails >= p.CheckTries || ctx.Err() != nil {
				done(l, fmt.Errorf("storage location can't be read (%d checks failed), not backed up: %w", l.fails, err))
				continue
			}
			report(protocol.BackupEventPayload{Repo: l.repo, Message: fmt.Sprintf(
				"check %d of %d failed, checking again in %ds: %v", l.fails, p.CheckTries, p.CheckRetrySecs, err)})
			l.ready = time.Now().Add(time.Duration(p.CheckRetrySecs) * time.Second)
			queue = append(queue, l)
			continue
		}
		done(l, backup(ctx, l.repo))
	}
	out, _ := json.Marshal(results)
	if failed > 0 {
		return string(out), fmt.Errorf("%d of %d storage locations not backed up", failed, len(p.Repos))
	}
	return string(out), nil
}

// checkRepo checks one repo can be read: `pgbackrest info` reads its index
// files and reports a status per repo (exiting 0 even when one is broken).
func checkRepo(ctx context.Context, stanza string, repo int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := pgbackrest(ctx, stanza, "info", fmt.Sprintf("--repo=%d", repo), "--output=json")
	if err != nil {
		return fmt.Errorf("pgbackrest info: %w: %s", err, strings.TrimSpace(out))
	}
	var info []struct {
		Name string `json:"name"`
		Repo []struct {
			Key    int `json:"key"`
			Status struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"status"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return fmt.Errorf("reading pgbackrest info: %w", err)
	}
	for _, st := range info {
		if st.Name != stanza {
			continue
		}
		for _, r := range st.Repo {
			if r.Key == repo {
				if !protocol.PGBackRestRepoReadable(r.Status.Code) {
					return fmt.Errorf("pgbackrest info: %s (code %d)", r.Status.Message, r.Status.Code)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("pgbackrest info doesn't list repo %d of stanza %s", repo, stanza)
}

// RunPGBackRestCatalog returns the raw JSON output of `pgbackrest info`.
// The conductor parses this to populate the backup catalog cache and
// determine the oldest/newest available restore points for the UI.
func RunPGBackRestCatalog(ctx context.Context, params protocol.PGBackRestCatalogParams) (string, error) {
	stanza := params.Stanza
	if stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}

	out, err := pgbackrest(ctx, stanza, "info", "--output=json")
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
func RunPGBackRestRestore(ctx context.Context, params protocol.PGBackRestRestoreParams) (string, error) {
	if params.Stanza == "" {
		return "", fmt.Errorf("stanza is required")
	}
	if params.TargetTime == "" {
		return "", fmt.Errorf("target_time is required")
	}
	// Validate before stopping anything so a bad time can't leave Postgres down.
	pgbackrestTime, err := pgbackrestTargetTime(params.TargetTime)
	if err != nil {
		return "", err
	}

	dataDir := params.DataDir
	if dataDir == "" {
		dataDir = PostgresDataDir(ctx)
	}

	var log strings.Builder

	slog.Info("restore: step 1 — stopping postgres", "data_dir", dataDir)
	// Step 1: Stop PostgreSQL — Patroni is paused so it will not restart it.
	if stopOut, err := stopPostgresql(ctx, dataDir); err != nil {
		// Log and continue — may already be stopped.
		slog.Warn("restore: postgres stop error (continuing)", "err", err, "out", stopOut)
		log.WriteString(fmt.Sprintf("postgres stop (may already be stopped): %v\n%s\n", err, stopOut))
	} else {
		slog.Info("restore: postgres stopped")
		log.WriteString("postgres stopped\n")
	}

	// Step 2: Run pgBackRest restore as the postgres OS user.
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
	slog.Info("restore: step 2 — running pgbackrest", "args", restoreArgs)
	sudoArgs := append([]string{"-u", "postgres"}, restoreArgs...)
	restoreOut, err := Command(ctx, "sudo", sudoArgs...).CombinedOutput() //nolint:gosec
	if err != nil {
		slog.Error("restore: pgbackrest failed", "err", err, "out", string(restoreOut))
		return log.String() + string(restoreOut), fmt.Errorf("pgbackrest restore failed: %w", err)
	}
	slog.Info("restore: pgbackrest complete", "out", string(restoreOut))
	log.WriteString(fmt.Sprintf("pgbackrest restore complete\n%s\n", restoreOut))

	// Step 3: Start PostgreSQL in recovery mode.
	slog.Info("restore: step 3 — starting postgres", "data_dir", dataDir)
	pgctlOut, err := startPostgresql(ctx, dataDir)
	if err != nil {
		slog.Error("restore: postgres start failed", "err", err, "out", pgctlOut)
		return log.String() + pgctlOut, fmt.Errorf("postgres start failed: %w\n%s", err, pgctlOut)
	}
	slog.Info("restore: postgres started", "out", pgctlOut)
	log.WriteString(fmt.Sprintf("postgres started\n%s\n", pgctlOut))

	// Step 4: Poll until PostgreSQL has promoted out of recovery.
	slog.Info("restore: step 4 — waiting for promotion")
	if err := waitForPromotion(ctx, 10*time.Minute); err != nil {
		slog.Error("restore: promotion timed out", "err", err)
		return log.String(), fmt.Errorf("waiting for promotion: %w", err)
	}
	slog.Info("restore: postgres promoted")
	log.WriteString("postgres promoted (no longer in recovery)\n")

	// Patroni resume is dispatched by the conductor after this task succeeds.
	return log.String(), nil
}

// RunPGBackRestTestPath checks a local-disk storage location works for both
// users that need it: postgres (pgBackRest, and the sync's writes) writes and
// removes a test file through the repo helper; the agent reads it back and
// lists the folder, as a backup sync does.
func RunPGBackRestTestPath(ctx context.Context, params protocol.PGBackRestTestPathParams) (string, error) {
	root := filepath.Clean(params.Path)
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("path must be absolute: %q", params.Path)
	}
	if err := runRepoHelper(ctx, "", "test", root); err != nil {
		return "", fmt.Errorf("postgres can't write to %s: %w", root, err)
	}
	got, err := os.ReadFile(filepath.Join(root, pathTestFile))
	if err == nil && string(got) != pathTestValue {
		err = errors.New("the test file read back differently")
	}
	if err == nil {
		_, err = os.ReadDir(root)
	}
	rmErr := runRepoHelper(ctx, "", "untest", root)
	if err != nil {
		return "", fmt.Errorf("netbox-agent can't read %s: %w", root, err)
	}
	if rmErr != nil {
		return "", fmt.Errorf("postgres can't remove files in %s: %w", root, rmErr)
	}
	return fmt.Sprintf("path %s works", root), nil
}

// pgbackrest runs the pgbackrest binary as the postgres OS user with the given
// stanza and additional arguments. Returns combined stdout+stderr.
func pgbackrest(ctx context.Context, stanza string, subcommand string, extraArgs ...string) (string, error) {
	args := append([]string{"-u", "postgres", "pgbackrest", "--stanza=" + stanza, subcommand}, extraArgs...)
	out, err := Command(ctx, "sudo", args...).CombinedOutput()
	return string(out), err
}

// stopPostgresql stops the PostgreSQL process via pg_ctl (cross-distro).
// postmaster.pid lives in the data dir regardless of which process started PostgreSQL,
// so this works whether Patroni or systemd started the server.
func stopPostgresql(ctx context.Context, dataDir string) (string, error) {
	out, err := Command(ctx, "sudo", "-u", "postgres",
		"pg_ctl", "stop", "-D", dataDir, "-m", "fast", "-w").CombinedOutput()
	return string(out), err
}

// startPostgresql starts PostgreSQL in a distro-portable way.
// On RHEL/Arch, postgresql.conf lives inside the data dir — plain pg_ctl works.
// On Debian/Ubuntu, it lives in /etc/postgresql/<ver>/<cluster>/ and must be
// passed via -o "-c config_file=...".
func startPostgresql(ctx context.Context, dataDir string) (string, error) {
	args := []string{"-u", "postgres", "pg_ctl", "start", "-D", dataDir, "-w", "-t", "120"}
	if _, err := os.Stat(filepath.Join(dataDir, "postgresql.conf")); err != nil {
		if confFile := findDebianConfFile(ctx, dataDir); confFile != "" {
			args = append(args, "-o", "-c config_file="+confFile)
		}
	}
	// CombinedOutput() creates a pipe; postgres (a daemon) inherits the write end
	// from pg_ctl and holds it open, so cmd.Wait() never returns. A real *os.File
	// avoids creating a pipe — the fd is inherited by postgres harmlessly.
	tmp, err := os.CreateTemp("", "pg_ctl_start_*.log")
	if err != nil {
		return "", fmt.Errorf("pg_ctl start: create temp log: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	cmd := Command(ctx, "sudo", args...)
	cmd.Stdout = tmp
	cmd.Stderr = tmp
	runErr := cmd.Run()
	_, _ = tmp.Seek(0, io.SeekStart)
	out, _ := io.ReadAll(tmp)
	return string(out), runErr
}

// findDebianConfFile uses pg_lsclusters to locate postgresql.conf for a given
// data directory. Returns "" on non-Debian systems or if not found.
func findDebianConfFile(ctx context.Context, dataDir string) string {
	out, err := Command(ctx, "pg_lsclusters", "-h").Output()
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
func waitForPromotion(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := Command(ctx,
			"sudo", "-u", "postgres", "psql", "-Atc", "SELECT pg_is_in_recovery()", "postgres",
		).CombinedOutput()
		result := strings.TrimSpace(string(out))
		slog.Info("waitForPromotion: poll", "result", result, "err", err)
		if err == nil && result == "f" {
			return nil
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("timed out after %s waiting for PostgreSQL to promote", timeout)
}

// PostgresDataDir attempts to detect the PostgreSQL data directory from
// pg_lsclusters (Debian/Ubuntu). Falls back to the standard path.
func PostgresDataDir(ctx context.Context) string {
	out, err := Command(ctx, "pg_lsclusters", "-h").Output()
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

// pgbackrestTargetTime converts an RFC3339 time to the "YYYY-MM-DD HH:MM:SS+00"
// form pgbackrest expects, in UTC. Fractional seconds are dropped.
func pgbackrestTargetTime(s string) (string, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", fmt.Errorf("invalid target_time %q (must be RFC3339): %w", s, err)
	}
	return t.UTC().Format("2006-01-02 15:04:05-07"), nil
}
