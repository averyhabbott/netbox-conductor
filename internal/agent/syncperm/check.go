// Package syncperm implements the "check-sync-permissions" agent CLI command.
// It fetches the cluster's configured sync paths from the conductor and ensures
// the netbox-agent OS user has the necessary filesystem access on each one.
// The command must be run with sudo (or as root) since it modifies group membership.
package syncperm

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"

	agentconfig "github.com/averyhabbott/netbox-conductor/internal/agent/config"
)

const agentUser = "netbox-agent"

// syncConfig is the response body from GET /api/v1/agent/sync-config.
type syncConfig struct {
	MediaSyncEnabled        bool     `json:"media_sync_enabled"`
	ExtraFoldersSyncEnabled bool     `json:"extra_folders_sync_enabled"`
	ExtraSyncFolders        []string `json:"extra_sync_folders"`
}

// Run fetches the cluster's sync config from the conductor and checks/fixes
// filesystem permissions on all configured sync paths so that netbox-agent
// can read from (source node) and write to (target node) each directory.
func Run(cfg *agentconfig.Config) error {
	sc, err := fetchSyncConfig(cfg)
	if err != nil {
		return fmt.Errorf("fetching sync config from conductor: %w", err)
	}

	// Always check NETBOX_MEDIA_ROOT; add extra folders when enabled.
	paths := []string{cfg.NetboxMediaRoot}
	if sc.ExtraFoldersSyncEnabled {
		for _, p := range sc.ExtraSyncFolders {
			if p != "" {
				paths = append(paths, p)
			}
		}
	}

	fmt.Printf("Checking sync permissions for %d path(s):\n", len(paths))

	anyError := false
	for _, path := range paths {
		if err := checkAndFix(path); err != nil {
			fmt.Printf("  ✗ %s: %v\n", path, err)
			anyError = true
		}
	}

	if anyError {
		return fmt.Errorf("one or more paths could not be fixed — review the output above")
	}
	fmt.Println("\nAll paths OK. Note: group membership changes take effect on the next agent restart.")
	return nil
}

// fetchSyncConfig calls the conductor REST API using the agent's bearer token.
func fetchSyncConfig(cfg *agentconfig.Config) (*syncConfig, error) {
	// Convert wss:// → https:// and strip the WebSocket path suffix.
	baseURL := strings.NewReplacer("wss://", "https://", "ws://", "http://").Replace(cfg.ServerURL)
	if idx := strings.Index(baseURL, "/api/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	httpClient, err := buildHTTPClient(cfg)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/agent/sync-config", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to conductor failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("conductor returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var sc syncConfig
	if err := json.NewDecoder(resp.Body).Decode(&sc); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &sc, nil
}

func buildHTTPClient(cfg *agentconfig.Config) (*http.Client, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // intentional dev option
	}
	if cfg.TLSCACert != "" {
		pemData, err := os.ReadFile(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("reading CA cert %s: %w", cfg.TLSCACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("no valid certificates in %s", cfg.TLSCACert)
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}, nil
}

// checkAndFix inspects path and ensures netbox-agent has read+execute access,
// either as a direct group member or by adding it to the directory's owning group.
func checkAndFix(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("path does not exist or is inaccessible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("expected a directory, got a regular file")
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("unable to read filesystem metadata (non-Linux platform?)")
	}

	groupName, err := groupNameForGID(int(stat.Gid))
	if err != nil {
		return fmt.Errorf("resolving group for GID %d: %w", stat.Gid, err)
	}

	member, err := isGroupMember(agentUser, groupName)
	if err != nil {
		return fmt.Errorf("checking group membership: %w", err)
	}

	mode := info.Mode().Perm()
	groupCanRead := mode&0o040 != 0
	groupCanExec := mode&0o010 != 0

	if member && groupCanRead && groupCanExec {
		fmt.Printf("  ✓ %s — %s already has access (group: %s, mode: %04o)\n",
			path, agentUser, groupName, mode)
		return nil
	}

	if !member {
		fmt.Printf("  → Adding %s to group '%s'\n", agentUser, groupName)
		out, err := exec.Command("usermod", "-aG", groupName, agentUser).CombinedOutput()
		if err != nil {
			return fmt.Errorf("usermod -aG %s %s failed: %w\n%s", groupName, agentUser, err, string(out))
		}
	}

	if !groupCanRead || !groupCanExec {
		fmt.Printf("  → Applying g+rX on %s (current mode: %04o)\n", path, mode)
		out, err := exec.Command("chmod", "g+rX", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("chmod g+rX %s failed: %w\n%s", path, err, string(out))
		}
	}

	fmt.Printf("  ✓ %s — fixed (group: %s)\n", path, groupName)
	return nil
}

// groupNameForGID resolves a numeric GID to its name by reading /etc/group.
func groupNameForGID(gid int) (string, error) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return "", err
	}
	defer f.Close()

	target := fmt.Sprint(gid)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 4)
		if len(fields) >= 3 && fields[2] == target {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("GID %d not found in /etc/group", gid)
}

// isGroupMember checks /etc/group to see if username is listed as a member of groupName.
func isGroupMember(username, groupName string) (bool, error) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 4)
		if len(fields) < 4 || fields[0] != groupName {
			continue
		}
		for _, m := range strings.Split(fields[3], ",") {
			if strings.TrimSpace(m) == username {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}
