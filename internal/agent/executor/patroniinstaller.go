package executor

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// InstallPatroni installs Patroni using the package manager detected on the system,
// or runs the operator-supplied install command if provided.
func InstallPatroni(params protocol.PatroniInstallParams) (string, error) {
	if params.InstallCmd != "" {
		return runInstallCmd(params.InstallCmd)
	}

	pm := params.PackageManager
	if pm == "" {
		pm = detectPackageManager()
	}

	// Install commands require root; the agent runs as netbox-agent so we use sudo.
	var cmd *exec.Cmd
	switch pm {
	case "apt", "apt-get":
		cmd = exec.Command("sudo", "apt-get", "install", "-y", "patroni")
	case "yum":
		cmd = exec.Command("sudo", "yum", "install", "-y", "patroni")
	case "dnf":
		cmd = exec.Command("sudo", "dnf", "install", "-y", "patroni")
	default:
		return "", fmt.Errorf("unsupported package manager %q — set install_cmd to override", pm)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("patroni install failed: %w", err)
	}
	result := string(out)

	// pysyncobj is required for Patroni's built-in Raft DCS but is not pulled
	// in automatically by the apt package. Install it system-wide so both
	// 'patroni' (the main daemon) and 'patroni_raft_controller' can import it.
	// --break-system-packages is required on Python 3.11+ (PEP 668 systems).
	pipCmd := exec.Command("sudo", "pip3", "install", "--break-system-packages", "--quiet", "pysyncobj")
	if pipOut, pipErr := pipCmd.CombinedOutput(); pipErr != nil {
		result += "\nwarn: pysyncobj install failed: " + pipErr.Error() + "\n" + string(pipOut)
	} else {
		result += "\npysyncobj installed"
	}

	return result, nil
}

func detectPackageManager() string {
	for _, pm := range []string{"apt-get", "dnf", "yum"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
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
