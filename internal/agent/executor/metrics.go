// Package executor handles task execution and system metrics collection.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/abottVU/netbox-failover/internal/shared/protocol"
)

// MetricsCollector gathers system metrics and service state for heartbeats.
type MetricsCollector struct {
	netboxConfigPath string
	mediaRoot        string
	patroniRESTURL   string
	httpClient       *http.Client
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

// formatSI formats bytes as a human-readable string (for logging).
func formatSI(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	return fmt.Sprintf("%.1fKiB", float64(bytes)/1024)
}

var _ = formatSI // suppress unused
