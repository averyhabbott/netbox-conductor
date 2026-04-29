// Package executor handles task execution and system metrics collection.
package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// MetricsCollector gathers system metrics and service state for heartbeats.
type MetricsCollector struct {
	netboxConfigPath string
	mediaRoot        string
	patroniRESTURL   string
	httpClient       *http.Client
	cachedNBVersion  string // cached after first successful detection
}

// NewMetricsCollector creates a collector using the given config paths.
func NewMetricsCollector(netboxConfigPath, mediaRoot, patroniRESTURL string) *MetricsCollector {
	return &MetricsCollector{
		netboxConfigPath: netboxConfigPath,
		mediaRoot:        mediaRoot,
		patroniRESTURL:   patroniRESTURL,
		httpClient:       &http.Client{Timeout: 3 * time.Second},
	}
}

// Collect returns a HeartbeatPayload populated with current system state.
func (m *MetricsCollector) Collect() (protocol.HeartbeatPayload, error) {
	hb := protocol.HeartbeatPayload{}

	// Load averages
	if avg, err := load.Avg(); err == nil {
		hb.LoadAvg1 = avg.Load1
		hb.LoadAvg5 = avg.Load5
	}

	// Memory
	if vm, err := mem.VirtualMemory(); err == nil {
		hb.MemUsedPct = vm.UsedPercent
	}

	// Disk usage on media root mount
	if du, err := disk.Usage(m.mediaRoot); err == nil {
		hb.DiskUsedPct = du.UsedPercent
	}

	// CPU (non-blocking — just captures for context; load avg is more useful)
	_, _ = cpu.Percent(0, false)

	// Service state
	hb.NetboxRunning = isServiceActive("netbox")
	hb.RQRunning = isServiceActive("netbox-rq")
	hb.RedisRunning = isServiceActive("redis")
	hb.RedisRole = queryRedisRole()
	hb.SentinelRunning = isServiceActive("redis-sentinel")
	hb.PatroniRunning = isServiceActive("patroni")
	hb.PostgresRunning = isPostgresReady()

	// NetBox version (cached; re-detected if empty)
	if m.cachedNBVersion == "" {
		m.cachedNBVersion = detectNetboxVersion(m.netboxConfigPath)
	}
	hb.NetboxVersion = m.cachedNBVersion

	// Patroni state
	role, lagBytes, stateJSON := m.queryPatroni()
	hb.PatroniRole = role
	if lagBytes >= 0 {
		hb.PatroniLagB = &lagBytes
	}
	if stateJSON != nil {
		raw := json.RawMessage(stateJSON)
		hb.PatroniState = &raw
	}

	return hb, nil
}

// queryRedisRole calls the local redis-cli and returns "master", "slave", or "".
// If Redis has requirepass set, the password is read from redis.conf and passed to
// redis-cli so that the ROLE command succeeds after ConfigureFailover sets auth.
func queryRedisRole() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := []string{"-p", "6379"}
	if pass := redisPassword(); pass != "" {
		args = append(args, "--no-auth-warning", "-a", pass)
	}
	args = append(args, "ROLE")
	out, err := exec.CommandContext(ctx, "redis-cli", args...).Output()
	if err != nil {
		return ""
	}
	parts := strings.Fields(string(out))
	if len(parts) > 0 {
		return strings.ToLower(parts[0])
	}
	return ""
}

// redisPassword reads the requirepass value from redis.conf, returning "" if not set.
// The agent is in the redis group and redis.conf is group-readable (640).
func redisPassword() string {
	data, err := os.ReadFile("/etc/redis/redis.conf")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "requirepass ") {
			return strings.TrimPrefix(line, "requirepass ")
		}
	}
	return ""
}

// isPostgresReady uses pg_isready to check whether Postgres is accepting connections.
// More reliable than checking the systemd unit name, which varies across distro versions.
func isPostgresReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "pg_isready", "-q").Run() == nil
}

// isServiceActive checks if a systemd service is active.
func isServiceActive(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).CombinedOutput()
	_ = out
	return err == nil
}

// queryPatroni calls the local Patroni REST API and extracts role + lag.
// Returns ("", -1, nil) if Patroni is not running or not configured.
func (m *MetricsCollector) queryPatroni() (role string, lagBytes int64, stateJSON []byte) {
	if m.patroniRESTURL == "" {
		return "", -1, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.patroniRESTURL+"/patroni", nil)
	if err != nil {
		return "", -1, nil
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", -1, nil
	}
	defer resp.Body.Close()

	var state map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return "", -1, nil
	}

	stateJSON, _ = json.Marshal(state)

	if r, ok := state["role"].(string); ok {
		role = r
	}

	// Replication lag is in xlog.received_diff_bytes or replication_state
	if xlog, ok := state["xlog"].(map[string]any); ok {
		if diff, ok := xlog["received_diff_bytes"]; ok {
			switch v := diff.(type) {
			case float64:
				lagBytes = int64(v)
			}
		}
	}
	if lagBytes == 0 {
		lagBytes = -1 // -1 = not applicable (primary or no lag info)
	}

	return role, lagBytes, stateJSON
}

// PatroniRoleWatcher watches for role changes and calls onChange when detected.
type PatroniRoleWatcher struct {
	collector  *MetricsCollector
	lastRole   string
	onChange   func(newRole, prevRole string, stateJSON []byte)
}

// NewPatroniRoleWatcher creates a watcher. onChange is called when the role changes.
func NewPatroniRoleWatcher(c *MetricsCollector, onChange func(newRole, prevRole string, stateJSON []byte)) *PatroniRoleWatcher {
	return &PatroniRoleWatcher{collector: c, onChange: onChange}
}

// Poll checks the current Patroni role and triggers onChange if it changed.
func (w *PatroniRoleWatcher) Poll() {
	role, _, stateJSON := w.collector.queryPatroni()
	if role == "" {
		return
	}
	role = strings.ToLower(role)
	if role != w.lastRole {
		prev := w.lastRole
		w.lastRole = role
		if w.onChange != nil && prev != "" { // skip initial population
			w.onChange(role, prev, stateJSON)
		} else if prev == "" {
			w.lastRole = role // silent first population
		}
	}
}

// detectNetboxVersion returns the NetBox version string by reading well-known
// version files relative to the configuration.py path.
// Returns "" if the version cannot be determined.
func detectNetboxVersion(configPath string) string {
	if configPath == "" {
		return ""
	}
	// configPath is typically /opt/netbox/netbox/netbox/configuration.py
	// NetBox repo root is three levels up: /opt/netbox/
	dir := filepath.Dir(configPath)         // /opt/netbox/netbox/netbox
	pkgDir := filepath.Dir(dir)             // /opt/netbox/netbox
	repoRoot := filepath.Dir(pkgDir)        // /opt/netbox

	// Try release.py: VERSION = (4, 1, 0)  or  VERSION = "4.1.0"
	if v := parseVersionFile(filepath.Join(dir, "release.py")); v != "" {
		return v
	}
	// Try __init__.py with same patterns
	if v := parseVersionFile(filepath.Join(dir, "__init__.py")); v != "" {
		return v
	}
	// Try one level up (netbox package __init__.py)
	if v := parseVersionFile(filepath.Join(pkgDir, "__init__.py")); v != "" {
		return v
	}
	// NetBox v4+ stores version in pyproject.toml at the repo root
	if v := parsePyprojectVersion(filepath.Join(repoRoot, "pyproject.toml")); v != "" {
		return v
	}
	return ""
}

var rePyprojectVersion = regexp.MustCompile(`(?m)^\s*version\s*=\s*["'](\d+\.\d+\.\d+[^"']*)["']`)

func parsePyprojectVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := rePyprojectVersion.Find(data); m != nil {
		if sub := rePyprojectVersion.FindSubmatch(data); len(sub) > 1 {
			return string(sub[1])
		}
	}
	return ""
}

var (
	reVersionTuple  = regexp.MustCompile(`VERSION\s*=\s*\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)`)
	reVersionString = regexp.MustCompile(`VERSION\s*=\s*["'](\d+\.\d+\.\d+[^"']*)["']`)
)

func parseVersionFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if m := reVersionTuple.FindStringSubmatch(line); m != nil {
			return m[1] + "." + m[2] + "." + m[3]
		}
		if m := reVersionString.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// formatSI formats bytes as a human-readable string (for logging).
func formatSI(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	return fmt.Sprintf("%.1fKiB", float64(bytes)/1024)
}

var _ = formatSI // suppress unused
