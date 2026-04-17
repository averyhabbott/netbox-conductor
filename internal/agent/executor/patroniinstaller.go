package executor

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// patroniDepsDir is pre-created by the agent installer (owned by netbox-agent).
// A patroni.service drop-in sets PYTHONPATH to this directory so Patroni finds
// pysyncobj without it being installed into the global Python distribution.
const patroniDepsDir = "/var/lib/netbox-agent/patroni-deps"

// InstallPatroni verifies Patroni is present and installs the pysyncobj package
// required for Patroni's built-in Raft DCS. Patroni and redis-sentinel are
// pre-installed by the agent installer (install.sh) running as root, so this
// task no longer needs sudo for package management.
func InstallPatroni(params protocol.PatroniInstallParams) (string, error) {
	if params.InstallCmd != "" {
		return runInstallCmd(params.InstallCmd)
	}

	// Verify patroni is installed (pre-installed by the agent installer).
	if _, err := exec.LookPath("patroni"); err != nil {
		return "", fmt.Errorf("patroni binary not found — re-run the agent installer to install it")
	}

	// pysyncobj is required for Patroni's built-in Raft DCS but is not pulled
	// in automatically by the apt package. Install into the pre-created deps dir
	// (owned by netbox-agent) — no sudo needed.
	pipCmd := exec.Command("pip3", "install", "--target", patroniDepsDir, "--quiet", "pysyncobj")
	if pipOut, pipErr := pipCmd.CombinedOutput(); pipErr != nil {
		return string(pipOut), fmt.Errorf("pysyncobj install failed: %w", pipErr)
	}

	return "pysyncobj installed", nil
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
