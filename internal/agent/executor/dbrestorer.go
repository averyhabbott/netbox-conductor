package executor

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// RunDBRestore executes a database restore operation.
// "pitr" — runs a configurable restore command targeting a specific recovery time.
func RunDBRestore(params protocol.DBRestoreParams) (string, error) {
	switch params.Method {
	case "pitr":
		return runPITR(params)
	default:
		return "", fmt.Errorf("unknown restore method: %q (must be 'pitr')", params.Method)
	}
}

// runPITR executes a point-in-time recovery.
// Uses the RestoreCmd if provided, otherwise falls back to pgBackRest.
func runPITR(params protocol.DBRestoreParams) (string, error) {
	// Validate target time format (ISO 8601)
	if params.TargetTime == "" {
		return "", fmt.Errorf("target_time is required for pitr")
	}
	if _, err := time.Parse(time.RFC3339, params.TargetTime); err != nil {
		return "", fmt.Errorf("invalid target_time %q (must be RFC3339, e.g. 2024-01-15T14:30:00Z): %w", params.TargetTime, err)
	}

	restoreCmd := params.RestoreCmd
	if restoreCmd == "" {
		// Default: pgBackRest restore targeting the given time.
		// Operators must have pgBackRest configured with a stanza named after the Patroni scope.
		stanza := params.PatroniScope
		if stanza == "" {
			stanza = "main"
		}
		restoreCmd = fmt.Sprintf(
			"pgbackrest --stanza=%s --type=time --target=%q --target-action=promote restore",
			stanza, params.TargetTime,
		)
	}

	parts := strings.Fields(restoreCmd)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty restore command")
	}

	cmd := exec.Command(parts[0], parts[1:]...) //nolint:gosec — operator-supplied command, admin-only
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pitr restore failed: %w", err)
	}
	return string(out), nil
}
