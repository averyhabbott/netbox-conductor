// Package patroni manages Patroni witness subprocesses, one per active_standby cluster.
package patroni

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultWitnessScript = "/opt/netbox-tool/patroni-witness.py"
	defaultPythonBin     = "/opt/netbox-tool/venv/bin/python3"
	defaultBasePort      = 5500
)

// WitnessConfig holds global witness configuration.
type WitnessConfig struct {
	ScriptPath  string // path to patroni-witness.py
	PythonBin   string // python interpreter (venv preferred)
	ServerAddr  string // tool server's bind address (e.g. "192.168.139.240")
	BasePort    int    // first port to allocate; each cluster gets BasePort+N
}

// WitnessManager manages one pysyncobj witness process per cluster.
type WitnessManager struct {
	cfg      WitnessConfig
	mu       sync.Mutex
	procs    map[uuid.UUID]*witnessProc
	nextPort int
}

type witnessProc struct {
	clusterID uuid.UUID
	addr      string // "host:port" this witness listens on
	partners  []string
	cmd       *exec.Cmd
	cancel    context.CancelFunc
}

// NewWitnessManager creates a manager with the given config.
func NewWitnessManager(cfg WitnessConfig) *WitnessManager {
	if cfg.ScriptPath == "" {
		cfg.ScriptPath = defaultWitnessScript
	}
	if cfg.PythonBin == "" {
		cfg.PythonBin = defaultPythonBin
		// Fall back to system python3 if venv doesn't exist
		if _, err := os.Stat(cfg.PythonBin); err != nil {
			cfg.PythonBin = "python3"
		}
	}
	if cfg.BasePort == 0 {
		cfg.BasePort = defaultBasePort
	}

	return &WitnessManager{
		cfg:      cfg,
		procs:    make(map[uuid.UUID]*witnessProc),
		nextPort: cfg.BasePort,
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

	port := m.nextPort
	m.nextPort++

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

// runProc supervises a single witness subprocess with auto-restart.
func (m *WitnessManager) runProc(proc *witnessProc) {
	backoff := 5 * time.Second

	for {
		// Check if we've been stopped
		m.mu.Lock()
		if _, ok := m.procs[proc.clusterID]; !ok {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		ctx, cancel := context.WithCancel(context.Background())
		proc.cancel = cancel

		scriptPath := m.cfg.ScriptPath
		if _, err := os.Stat(scriptPath); err != nil {
			// Script not installed yet — wait and retry
			log.Printf("witness script not found at %s — retrying in %s", scriptPath, backoff)
			cancel()
			time.Sleep(backoff)
			continue
		}

		args := append([]string{proc.addr}, proc.partners...)
		cmd := exec.CommandContext(ctx, m.cfg.PythonBin,
			append([]string{scriptPath}, args...)...)

		// Log witness stdout/stderr with a prefix
		cmd.Stdout = &prefixWriter{prefix: fmt.Sprintf("[witness %s] ", proc.clusterID)}
		cmd.Stderr = &prefixWriter{prefix: fmt.Sprintf("[witness %s] ", proc.clusterID)}

		// Ensure the script is in the right directory for relative imports
		cmd.Dir = filepath.Dir(scriptPath)

		proc.cmd = cmd
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return // stopped intentionally
			}
			log.Printf("witness crashed for cluster=%s: %v — restarting in %s",
				proc.clusterID, err, backoff)
		}

		cancel()

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
