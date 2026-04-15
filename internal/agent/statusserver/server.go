// Package statusserver runs a small HTTP server on the managed node so that
// reverse proxies and VIP health-checkers can determine whether this node is
// currently serving NetBox.
//
// Bind: 127.0.0.1:8081 (default; configured via AGENT_STATUS_ADDR in the agent
// env file). All public access goes through the node's nginx/Apache reverse
// proxy, which exposes GET /status on the HTTPS port. To allow direct access
// (e.g. for a remote HAProxy checking the agent port directly), set
// AGENT_STATUS_ADDR=0.0.0.0:8081.
//
// GET /status
//
//	200 OK            — node is healthy and eligible to serve traffic
//	503 Unavailable   — node is not eligible (NetBox down, or not Patroni primary)
//
// Health logic:
//   - Patroni not configured:       200 if netbox.service is active
//   - app_tier_always_available=true: 200 if netbox.service is active
//     (all nodes always serve; LB steers across all healthy nodes)
//   - app_tier_always_available=false: 200 if netbox.service is active AND
//     local Patroni reports this node as primary
//     (single active node; LB gates on DB write-eligibility)
//
// The response body is always JSON so operators can inspect individual service
// states without logging into the host:
//
//	{"status":"ok","netbox":true,"rq":true,"node_id":"<uuid>","patroni_primary":true}
package statusserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// State holds runtime cluster configuration delivered by the server on each
// WebSocket connect. The status handler reads it on every request.
// All methods are safe for concurrent use.
type State struct {
	mu                 sync.RWMutex
	patroniConfigured  bool
	appTierAlwaysAvail bool
	patroniRESTURL     string
}

// NewState creates a State with the given Patroni REST base URL
// (e.g. "http://127.0.0.1:8008"). Cluster config fields start as zero values
// until the first ServerHello is received.
func NewState(patroniRESTURL string) *State {
	return &State{patroniRESTURL: patroniRESTURL}
}

// Update overwrites the cluster config fields atomically.
func (s *State) Update(patroniConfigured, appTierAlwaysAvail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patroniConfigured = patroniConfigured
	s.appTierAlwaysAvail = appTierAlwaysAvail
}

// Serve starts the HTTP status server and blocks until ctx is cancelled, at
// which point it performs a graceful shutdown (5 s deadline).
// addr is the bind address, e.g. "127.0.0.1:8081" or "0.0.0.0:8081".
func Serve(ctx context.Context, addr string, nodeID string, state *State) {

	mux := http.NewServeMux()
	mux.HandleFunc("/status", makeStatusHandler(nodeID, state))

	srv := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
		// Keep WriteTimeout generous enough for two systemctl checks (2 s each)
		// plus the optional Patroni REST call (2 s) plus response serialisation.
		WriteTimeout: 10 * time.Second,
	}

	// Start listening first so we can report the error synchronously before
	// entering the goroutine, giving main() a chance to log a clear message.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("status server: failed to bind", "addr", addr, "error", err)
		return
	}
	slog.Info("status server listening", "addr", addr)

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("status server error", "error", err)
		}
	}()

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Warn("status server shutdown error", "error", err)
	}
}

type statusResponse struct {
	Status         string `json:"status"` // "ok" | "unavailable"
	Netbox         bool   `json:"netbox"`
	RQ             bool   `json:"rq"`
	NodeID         string `json:"node_id"`
	PatroniPrimary *bool  `json:"patroni_primary,omitempty"`
}

func makeStatusHandler(nodeID string, state *State) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state.mu.RLock()
		patroniConfigured := state.patroniConfigured
		appTierAlwaysAvail := state.appTierAlwaysAvail
		patroniRESTURL := state.patroniRESTURL
		state.mu.RUnlock()

		netboxUp := isActive("netbox")
		rqUp := isActive("netbox-rq")

		// In app_tier_always_available=false mode with Patroni configured,
		// only return 200 if this node is the Patroni primary. This ensures the
		// LB only routes to the node whose local Postgres is writable.
		var patroniPrimary *bool
		if patroniConfigured && !appTierAlwaysAvail {
			primary := isPatroniPrimary(patroniRESTURL)
			patroniPrimary = &primary
		}

		resp := statusResponse{
			Netbox:         netboxUp,
			RQ:             rqUp,
			NodeID:         nodeID,
			PatroniPrimary: patroniPrimary,
		}

		// A node is healthy for traffic if:
		//   1. netbox.service is active, AND
		//   2. (if in Patroni primary-gated mode) this node is the primary
		healthy := netboxUp
		if patroniPrimary != nil && !*patroniPrimary {
			healthy = false
		}

		var code int
		if healthy {
			resp.Status = "ok"
			code = http.StatusOK
		} else {
			resp.Status = "unavailable"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// isActive returns true if the named systemd unit is in the "active" state.
func isActive(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

// isPatroniPrimary calls the local Patroni REST API's /primary endpoint.
// Patroni returns 200 if this node is the current primary, 503 if it is a replica.
// Returns false on any error (including Patroni not running), so nodes that
// haven't completed Patroni setup don't accidentally answer 200 to health checks.
func isPatroniPrimary(restURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, restURL+"/primary", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
