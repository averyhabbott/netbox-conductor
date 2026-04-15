package executor

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// EnforceRetention runs the backup expire / retention enforcement command.
// By default it calls pgBackRest expire for the given stanza.
// Operators can override with a custom command via params.ExpireCmd.
func EnforceRetention(params protocol.EnforceRetentionParams) (string, error) {
	if params.ExpireCmd != "" {
		return runExpireCmd(params.ExpireCmd)
	}

	stanza := params.PatroniScope
	if stanza == "" {
		stanza = "main"
	}

	cmd := exec.Command("pgbackrest", "--stanza="+stanza, "expire")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pgbackrest expire failed: %w", err)
	}
	return string(out), nil
}

func runExpireCmd(cmd string) (string, error) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty expire command")
	}
	c := exec.Command(parts[0], parts[1:]...) //nolint:gosec — operator-supplied, admin-only
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("expire command failed: %w", err)
	}
	return string(out), nil
}
