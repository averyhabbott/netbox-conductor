package executor

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// CreatePgRole creates (or updates the password of) a PostgreSQL role.
// Executed as the postgres OS user via peer authentication — no pg_hba.conf
// remote-access rules or database passwords are required.
//
// Retries for up to 60 seconds when PostgreSQL is not yet accepting
// connections, which happens when this task runs immediately after a
// Patroni restart (Patroni needs a few seconds to start PostgreSQL).
func CreatePgRole(params protocol.CreatePgRoleParams) (string, error) {
	if params.RoleName == "" {
		return "", fmt.Errorf("role_name is required")
	}

	opts := strings.Join(params.Options, " ")
	// Idempotent: create if absent, otherwise update the password so re-running
	// configure-failover always leaves the role in the correct state.
	sql := fmt.Sprintf(`DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN
    CREATE ROLE %s WITH %s PASSWORD '%s';
  ELSE
    ALTER ROLE %s WITH %s PASSWORD '%s';
  END IF;
END $$;`,
		params.RoleName,
		params.RoleName, opts, params.Password,
		params.RoleName, opts, params.Password)

	deadline := time.Now().Add(60 * time.Second)
	for {
		cmd := exec.Command("sudo", "-u", "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-c", sql)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return fmt.Sprintf("role %s created/updated", params.RoleName), nil
		}
		if pgIsUnavailable(out) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Second)
			continue
		}
		return string(out), fmt.Errorf("create role %s failed: %w", params.RoleName, err)
	}
}

// pgIsUnavailable returns true when psql/pg_dump output indicates PostgreSQL
// is not yet listening — the caller should retry rather than fail permanently.
func pgIsUnavailable(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "No such file or directory") ||
		strings.Contains(s, "Connection refused") ||
		strings.Contains(s, "could not connect")
}
