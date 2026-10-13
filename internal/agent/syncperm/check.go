// Package syncperm implements the "check-sync-permissions" agent CLI command.
// It fetches the cluster's configured sync paths from the conductor and grants
// the netbox-agent OS user read/write access to each one with POSIX ACLs.
// The command must be run with sudo (or as root) since it changes ACLs.
package syncperm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strings"

	agentconfig "github.com/averyhabbott/netbox-conductor/internal/agent/config"
)

const (
	agentUser  = "netbox-agent"
	netboxUser = "netbox" // the NetBox service user; gets the same access to files the agent copies in
)

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

	// The NetBox media folder (none on a db-only node), plus extra folders when enabled.
	var paths []string
	if cfg.NetboxMediaRoot != "" {
		paths = append(paths, cfg.NetboxMediaRoot)
	}
	if sc.ExtraFoldersSyncEnabled {
		for _, p := range sc.ExtraSyncFolders {
			if p != "" {
				paths = append(paths, p)
			}
		}
	}

	if _, err := exec.LookPath("setfacl"); err != nil {
		return errors.New("setfacl not found: install the 'acl' package (apt-get install acl / dnf install acl)")
	}
	users := []string{agentUser}
	if _, err := user.Lookup(netboxUser); err == nil {
		users = append(users, netboxUser)
	} else {
		fmt.Printf("Note: user %q not found; granting access to %s only.\n", netboxUser, agentUser)
	}

	fmt.Printf("Granting sync access for %d path(s):\n", len(paths))

	anyError := false
	for _, path := range paths {
		if err := grant(path, users); err != nil {
			fmt.Printf("  ✗ %s: %v\n", path, err)
			anyError = true
		}
	}

	if anyError {
		return fmt.Errorf("one or more paths could not be fixed — review the output above")
	}
	fmt.Println("\nAll paths OK. ACLs take effect immediately; no restart needed.")
	return nil
}

// fetchSyncConfig calls the conductor REST API using the agent's bearer token.
func fetchSyncConfig(cfg *agentconfig.Config) (*syncConfig, error) {
	httpClient, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, cfg.BaseURL()+"/api/v1/agent/sync-config", nil)
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

// grant gives each user read/write on path and everything under it, plus
// default ACL entries so files and dirs created later inherit the same access.
// rwX: execute only on directories (and files already executable).
func grant(path string, users []string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("path does not exist or is inaccessible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("expected a directory, got a regular file")
	}
	var spec []string
	for _, u := range users {
		spec = append(spec, "u:"+u+":rwX", "d:u:"+u+":rwX")
	}
	args := []string{"-R", "-m", strings.Join(spec, ","), path}
	if out, err := exec.Command("setfacl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("setfacl %s failed: %w\n%s", strings.Join(args, " "), err, out)
	}
	fmt.Printf("  ✓ %s — read/write for %s (setfacl %s)\n", path, strings.Join(users, ", "), strings.Join(args, " "))
	return nil
}
