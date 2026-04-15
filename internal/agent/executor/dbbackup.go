package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const (
	defaultBackupDB      = "netbox"
	defaultBackupUser    = "postgres"
	defaultBackupDir     = "/var/lib/postgresql/backups"
	backupDirPerm        = 0o700
)

// RunDBBackup runs pg_dump against the local Postgres instance and writes a
// custom-format dump file to params.OutputDir (default /var/lib/postgresql/backups).
// Custom format (-Fc) is used because it is smaller than plain SQL, supports
// parallel restore, and allows selective table restores — better than plain SQL
// for the typical NetBox database sizes.
//
// The full path of the dump file is returned as the task output so it can be
// surfaced in the conductor UI and used by the future restore-from-backup feature.
func RunDBBackup(params protocol.DBBackupParams) (string, error) {
	dbName := params.DBName
	if dbName == "" {
		dbName = defaultBackupDB
	}
	dbUser := params.DBUser
	if dbUser == "" {
		dbUser = defaultBackupUser
	}
	outputDir := params.OutputDir
	if outputDir == "" {
		outputDir = defaultBackupDir
	}

	// Create the backup directory as the postgres OS user so peer auth works and
	// the directory lands in postgres-owned space.
	if mkdirOut, err := exec.Command("sudo", "-u", "postgres", "mkdir", "-p", outputDir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("creating backup directory %s: %w\n%s", outputDir, err, mkdirOut)
	}

	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.dump", dbName, timestamp)
	destPath := filepath.Join(outputDir, filename)

	// Run pg_dump as the postgres OS user so peer authentication works without
	// needing a password. The -U flag selects the Postgres role; peer auth maps
	// the OS user (postgres) to that role when they match.
	cmd := exec.Command(
		"sudo", "-u", "postgres",
		"pg_dump",
		"-Fc",
		"-U", dbUser,
		"-f", destPath,
		"-v",
		dbName,
	)
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Remove the partial dump so it is not mistaken for a valid backup.
		_ = os.Remove(destPath)
		return string(out), fmt.Errorf("pg_dump failed: %w\n%s", err, out)
	}

	info, err := os.Stat(destPath)
	if err != nil {
		return string(out), fmt.Errorf("backup written but stat failed: %w", err)
	}

	summary := fmt.Sprintf("backup complete: %s (%d bytes)\n%s", destPath, info.Size(), string(out))
	return summary, nil
}
