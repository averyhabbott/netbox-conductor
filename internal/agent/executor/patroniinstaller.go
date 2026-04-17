package executor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// patroniVenv is the Python venv created by the agent installer (install.sh).
// Owned by netbox-agent (world-readable/executable) so:
//   - this task can pip-install pysyncobj without sudo
//   - the postgres user (who runs patroni) can execute the venv binaries
const patroniVenv = "/opt/netbox-agent/venv"

// InstallPatroni verifies the Patroni venv is present and installs the
// pysyncobj package required for Patroni's built-in Raft DCS.
// Both patroni and pysyncobj are pre-installed into the venv by install.sh;
// this task re-runs pip install to ensure pysyncobj is current.
func InstallPatroni(params protocol.PatroniInstallParams) (string, error) {
	if params.InstallCmd != "" {
		return runInstallCmd(params.InstallCmd)
	}

	// Verify the venv patroni binary is present (created by the agent installer).
	patroniBin := patroniVenv + "/bin/patroni"
	if _, err := os.Stat(patroniBin); err != nil {
		return "", fmt.Errorf("patroni not found at %s — re-run the agent installer", patroniBin)
	}

	// Install (or upgrade) pysyncobj into the venv. The venv is owned by
	// netbox-agent so no sudo is required.
	pipCmd := exec.Command(patroniVenv+"/bin/pip", "install", "--quiet", "pysyncobj", "psycopg[binary]")
	if pipOut, pipErr := pipCmd.CombinedOutput(); pipErr != nil {
		return string(pipOut), fmt.Errorf("pysyncobj install failed: %w", pipErr)
	}

	return "pysyncobj installed to venv", nil
}

func runInstallCmd(cmd string) (string, error) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty install command")
	}
	c := exec.Command(parts[0], parts[1:]...) //nolint:gosec — operator-supplied, admin-only
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("install command failed: %w", err)
	}
	return string(out), nil
}
