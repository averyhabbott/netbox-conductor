// Package patroni manages Patroni raft-controller subprocesses, one per active_standby cluster.
package patroni

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/google/uuid"
)

const (
	defaultRaftControllerBin = "/opt/netbox-conductor/venv/bin/patroni_raft_controller"
	defaultRaftDataDir       = "/var/lib/netbox-conductor/raft"
	defaultBasePort          = 5500
)

// WitnessConfig holds global witness configuration.
type WitnessConfig struct {
	RaftControllerBin string // path to patroni_raft_controller binary
	RaftDataDir       string // base dir for per-cluster raft data (journal files)
	ServerAddr        string // tool server's bind address (e.g. "192.168.139.240")
	BasePort          int    // first port to allocate; each cluster gets BasePort+N
}

// WitnessManager manages one patroni_raft_controller process per cluster.
type WitnessManager struct {
	cfg   WitnessConfig
	db    *queries.WitnessPortQuerier
	mu    sync.Mutex
	procs map[uuid.UUID]*witnessProc
}

type witnessProc struct {
	clusterID uuid.UUID
	addr      string // "host:port" this witness listens on
	partners  []string
	cancel    context.CancelFunc
}

// NewWitnessManager creates a manager with the given config.
// db is required for persistent port allocation; if nil the manager falls back
// to sequential in-memory allocation (test/dev only).
func NewWitnessManager(cfg WitnessConfig, db *queries.WitnessPortQuerier) *WitnessManager {
	if cfg.RaftControllerBin == "" {
		cfg.RaftControllerBin = defaultRaftControllerBin
	}
	if cfg.RaftDataDir == "" {
		cfg.RaftDataDir = defaultRaftDataDir
	}
	if cfg.BasePort == 0 {
		cfg.BasePort = defaultBasePort
	}

	return &WitnessManager{
		cfg:   cfg,
		db:    db,
		procs: make(map[uuid.UUID]*witnessProc),
	}
}

// Start launches a witness for the given cluster if not already running.
// partnerAddrs are the Raft peer addresses of the data nodes ("host:5433").
func (m *WitnessManager) Start(clusterID uuid.UUID, partnerAddrs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.procs[clusterID]; ok {
		return nil // already running
	}

	if m.cfg.ServerAddr == "" {
		return fmt.Errorf("witness: SERVER_BIND_IP must be set to the conductor's reachable IP address")
	}

	port, err := m.allocatePort(clusterID)
	if err != nil {
		return fmt.Errorf("witness: port allocation failed: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", m.cfg.ServerAddr, port)

	proc := &witnessProc{
		clusterID: clusterID,
		addr:      addr,
		partners:  partnerAddrs,
	}
	m.procs[clusterID] = proc

	go m.runProc(proc)
	log.Printf("patroni witness started: cluster=%s addr=%s partners=%v", clusterID, addr, partnerAddrs)
	return nil
}

// allocatePort returns the persisted port for a cluster, allocating and storing
// a new one if none exists. Must be called with m.mu held.
func (m *WitnessManager) allocatePort(clusterID uuid.UUID) (int, error) {
	ctx := context.Background()

	if m.db != nil {
		// Try to load existing port first.
		if port, err := m.db.GetPort(ctx, clusterID); err == nil {
			return port, nil
		}
		// Allocate: find first unused port in range.
		used, err := m.db.ListPorts(ctx)
		if err != nil {
			return 0, err
		}
		inUse := make(map[int]bool, len(used))
		for _, p := range used {
			inUse[p] = true
		}
		for p := m.cfg.BasePort; p < m.cfg.BasePort+100; p++ {
			if !inUse[p] {
				if err := m.db.AllocatePort(ctx, clusterID, p); err != nil {
					return 0, err
				}
				return p, nil
			}
		}
		return 0, fmt.Errorf("no available witness ports in range %d–%d", m.cfg.BasePort, m.cfg.BasePort+99)
	}

	// Fallback: sequential in-memory allocation (no DB).
	port := m.cfg.BasePort
	for _, proc := range m.procs {
		if _, portStr, ok := parseAddr(proc.addr); ok && portStr >= port {
			port = portStr + 1
		}
	}
	return port, nil
}

// parseAddr extracts the numeric port from a "host:port" string.
func parseAddr(addr string) (string, int, bool) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			var port int
			if _, err := fmt.Sscanf(addr[i+1:], "%d", &port); err == nil {
				return addr[:i], port, true
			}
			return "", 0, false
		}
	}
	return "", 0, false
}

// CleanupData removes the raft data directory for a cluster.
// Call after Stop() when permanently deleting a cluster.
func (m *WitnessManager) CleanupData(clusterID uuid.UUID) error {
	dataDir := filepath.Join(m.cfg.RaftDataDir, clusterID.String())
	if err := os.RemoveAll(dataDir); err != nil {
		return fmt.Errorf("witness: removing raft data dir: %w", err)
	}
	return nil
}

// Stop terminates the witness for a cluster.
func (m *WitnessManager) Stop(clusterID uuid.UUID) {
	m.mu.Lock()
	proc, ok := m.procs[clusterID]
	if ok {
		delete(m.procs, clusterID)
	}
	m.mu.Unlock()

	if ok && proc.cancel != nil {
		proc.cancel()
		log.Printf("patroni witness stopped: cluster=%s", clusterID)
	}
}

// Addr returns the witness listen address for a cluster, or "" if not running.
func (m *WitnessManager) Addr(clusterID uuid.UUID) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.procs[clusterID]; ok {
		return p.addr
	}
	return ""
}

// RecoverAll restarts witnesses for all clusters passed in.
// Call at server startup to re-attach existing witnesses.
func (m *WitnessManager) RecoverAll(clusters []ClusterWitnessInfo) {
	for _, c := range clusters {
		if err := m.Start(c.ClusterID, c.PartnerAddrs); err != nil {
			log.Printf("witness recovery failed for cluster=%s: %v", c.ClusterID, err)
		}
	}
}

// ClusterWitnessInfo carries the info needed to (re)start a witness.
type ClusterWitnessInfo struct {
	ClusterID    uuid.UUID
	PartnerAddrs []string // Raft peer "host:port" addresses
}

// writeRaftConfig writes the patroni_raft_controller YAML config for this witness
// and returns the path to the written file.
func (m *WitnessManager) writeRaftConfig(proc *witnessProc) (string, error) {
	dataDir := filepath.Join(m.cfg.RaftDataDir, proc.clusterID.String())
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("creating raft data dir: %w", err)
	}

	var partners string
	for _, p := range proc.partners {
		partners += fmt.Sprintf("  - %s\n", p)
	}

	content := fmt.Sprintf(`raft:
  self_addr: %s
  partner_addrs:
%s  data_dir: %s
`, proc.addr, partners, dataDir)

	cfgPath := filepath.Join(dataDir, "raft-controller.yml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing raft config: %w", err)
	}
	return cfgPath, nil
}

// witness crash-loop guard parameters. A misconfigured binary or persistently
// invalid config would otherwise restart forever at the base 5s interval,
// flooding logs and burning CPU. After maxCrashes within crashWindow we back
// off to longBackoff and emit a one-shot error so it's visible in monitoring.
const (
	witnessMaxCrashes       = 5
	witnessCrashWindow      = time.Minute
	witnessLongBackoff      = 5 * time.Minute
	witnessSuccessThreshold = 30 * time.Second
)

// runProc supervises a single patroni_raft_controller subprocess with auto-restart.
//
// The restart loop has a crash-rate guard: more than witnessMaxCrashes failures
// inside witnessCrashWindow switches the loop to witnessLongBackoff and logs
// a sustained-failure error. A run that lasts at least witnessSuccessThreshold
// resets the counter, so an intermittent crash doesn't permanently degrade
// the restart cadence.
func (m *WitnessManager) runProc(proc *witnessProc) {
	const baseBackoff = 5 * time.Second
	backoff := baseBackoff

	var (
		recentCrashes  []time.Time
		degraded       bool
		degradedLogged bool
	)

	for {
		// Check if we've been stopped
		m.mu.Lock()
		if _, ok := m.procs[proc.clusterID]; !ok {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		controllerBin := m.cfg.RaftControllerBin
		if _, err := os.Stat(controllerBin); err != nil {
			slog.Error("witness: patroni_raft_controller not found", "path", controllerBin, "retry_in", backoff)
			time.Sleep(backoff)
			continue
		}

		cfgPath, err := m.writeRaftConfig(proc)
		if err != nil {
			slog.Error("witness: failed to write raft config", "cluster", proc.clusterID, "error", err, "retry_in", backoff)
			time.Sleep(backoff)
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		proc.cancel = cancel

		cmd := exec.CommandContext(ctx, controllerBin, cfgPath)
		cmd.Stdout = &prefixWriter{prefix: fmt.Sprintf("[witness %s] ", proc.clusterID)}
		cmd.Stderr = &prefixWriter{prefix: fmt.Sprintf("[witness %s] ", proc.clusterID)}

		startedAt := time.Now()
		runErr := cmd.Run()
		ranFor := time.Since(startedAt)

		if runErr != nil {
			if ctx.Err() != nil {
				cancel()
				return // stopped intentionally
			}
			log.Printf("witness crashed for cluster=%s after %s: %v — restarting in %s",
				proc.clusterID, ranFor.Round(time.Second), runErr, backoff)
		}

		cancel()

		// A run that stayed up long enough is treated as a successful supervision;
		// clear the crash record and return to the base backoff cadence.
		if ranFor >= witnessSuccessThreshold {
			recentCrashes = recentCrashes[:0]
			if degraded {
				slog.Info("witness: subprocess stabilized — returning to base backoff",
					"cluster", proc.clusterID, "ran_for", ranFor.Round(time.Second))
			}
			degraded = false
			degradedLogged = false
			backoff = baseBackoff
		} else {
			now := time.Now()
			recentCrashes = append(recentCrashes, now)
			cutoff := now.Add(-witnessCrashWindow)
			pruned := recentCrashes[:0]
			for _, t := range recentCrashes {
				if t.After(cutoff) {
					pruned = append(pruned, t)
				}
			}
			recentCrashes = pruned

			if len(recentCrashes) >= witnessMaxCrashes {
				if !degraded {
					degraded = true
				}
				backoff = witnessLongBackoff
				if !degradedLogged {
					slog.Error("witness: subprocess crash-looping — slowing restart to long backoff",
						"cluster", proc.clusterID,
						"recent_crashes", len(recentCrashes),
						"window", witnessCrashWindow.String(),
						"long_backoff", witnessLongBackoff.String())
					degradedLogged = true
				}
			}
		}

		// Check if stopped between crash and restart
		m.mu.Lock()
		if _, ok := m.procs[proc.clusterID]; !ok {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		time.Sleep(backoff)
	}
}

type prefixWriter struct {
	prefix string
}

func (w *prefixWriter) Write(p []byte) (n int, err error) {
	log.Printf("%s%s", w.prefix, string(p))
	return len(p), nil
}
