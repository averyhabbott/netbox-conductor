// Package ws manages the agent's outbound WebSocket connection to the tool server.
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/averyhabbott/netbox-conductor/internal/agent/config"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

const (
	minBackoff     = 5 * time.Second
	maxBackoff     = 120 * time.Second
	heartbeatEvery = 3 * time.Second
	agentVersion   = "0.1.0"
)

// MessageHandler is called for every inbound server message.
type MessageHandler func(ctx context.Context, env protocol.Envelope) error

// Client manages a persistent WebSocket connection to the tool server.
type Client struct {
	cfg        *config.Config
	onMessage  MessageHandler
	httpClient *http.Client

	// outbound is a buffered channel of envelopes to send to the server.
	outbound chan protocol.Envelope

	// HeartbeatFn is called each tick to produce heartbeat payload bytes.
	// The agent package sets this to a function that reads real system metrics.
	HeartbeatFn func() (protocol.HeartbeatPayload, error)

	// OnServerHello is called after every successful server.hello exchange.
	// Use it to propagate cluster config to other subsystems (e.g. status server).
	// Called on the connect goroutine; must not block.
	OnServerHello func(protocol.ServerHelloPayload)

	// OnDisconnect is called when an authenticated connection ends.
	OnDisconnect func()

	// tasks are the tasks received and not yet reported, listed in every
	// heartbeat. tasksMu also keeps a heartbeat's list and its place in
	// outbound together; see StartTask.
	tasksMu sync.Mutex
	tasks   map[string]bool
}

// New creates a Client. HeartbeatFn must be set before Run is called.
func New(cfg *config.Config, onMessage MessageHandler) (*Client, error) {
	httpClient, err := cfg.HTTPClient()
	if err != nil {
		return nil, fmt.Errorf("building HTTP client: %w", err)
	}
	return &Client{
		cfg:        cfg,
		onMessage:  onMessage,
		httpClient: httpClient,
		outbound:   make(chan protocol.Envelope, 64),
		tasks:      map[string]bool{},
	}, nil
}

// StartTask lists taskID in heartbeats, then sends ack. The conductor reads a
// task missing from a heartbeat that follows its ack as lost, so a task is
// listed before its ack is sent and until after its result is (EndTask).
func (c *Client) StartTask(taskID string, ack protocol.Envelope) {
	c.tasksMu.Lock()
	c.tasks[taskID] = true
	c.tasksMu.Unlock()
	c.Send(ack)
}

// EndTask sends a task's result (as SendWait), then stops listing the task.
func (c *Client) EndTask(taskID string, result protocol.Envelope, timeout time.Duration) error {
	err := c.SendWait(result, timeout)
	c.tasksMu.Lock()
	delete(c.tasks, taskID)
	c.tasksMu.Unlock()
	return err
}

// Send enqueues an envelope for delivery to the server.
func (c *Client) Send(env protocol.Envelope) {
	select {
	case c.outbound <- env:
	default:
		slog.Warn("outbound buffer full, dropping message")
	}
}

// SendWait enqueues an envelope, waiting up to timeout for buffer space instead
// of dropping it. Used for file-sync chunks, where a dropped message corrupts data.
func (c *Client) SendWait(env protocol.Envelope, timeout time.Duration) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case c.outbound <- env:
		return nil
	case <-t.C:
		return fmt.Errorf("outbound buffer still full after %s", timeout)
	}
}

// Run connects to the server and maintains the connection until ctx is cancelled.
// On disconnect it reconnects with exponential backoff + jitter.
func (c *Client) Run(ctx context.Context) {
	backoff := minBackoff
	for {
		if err := c.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return // context cancelled — clean shutdown
			}
			slog.Info("connection error, reconnecting", "error", err, "backoff", backoff.String())
		} else {
			slog.Info("disconnected, reconnecting")
			backoff = minBackoff // reset on clean disconnect
		}

		// Jitter: backoff ± 20%
		jitter := time.Duration(rand.Int63n(int64(backoff / 5)))
		sleep := backoff + jitter
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// connect dials the server, authenticates, then runs read and write pumps
// until the connection drops or ctx is cancelled.
func (c *Client) connect(ctx context.Context) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	defer dialCancel()

	conn, _, err := websocket.Dial(dialCtx, c.cfg.ServerURL, &websocket.DialOptions{
		HTTPClient: c.httpClient,
	})
	if err != nil {
		return fmt.Errorf("dialing %s: %w", c.cfg.ServerURL, err)
	}
	conn.SetReadLimit(4 * 1024 * 1024) // 4MB max message
	defer conn.CloseNow()

	slog.Info("connected", "server", c.cfg.ServerURL)

	// Send agent.hello
	hostname, _ := os.Hostname()
	helloPayload, _ := json.Marshal(protocol.AgentHelloPayload{
		NodeID:       c.cfg.NodeID,
		Token:        c.cfg.Token,
		AgentVersion: agentVersion,
		Hostname:     hostname,
		OS:           "linux",
		Arch:         runtime.GOARCH,
	})
	if err := wsjson.Write(ctx, conn, protocol.Envelope{
		ID:      newID(),
		Type:    protocol.TypeAgentHello,
		Payload: json.RawMessage(helloPayload),
	}); err != nil {
		return fmt.Errorf("sending agent.hello: %w", err)
	}

	// Read server.hello
	authCtx, authCancel := context.WithTimeout(ctx, 15*time.Second)
	defer authCancel()

	var serverHelloEnv protocol.Envelope
	if err := wsjson.Read(authCtx, conn, &serverHelloEnv); err != nil {
		return fmt.Errorf("reading server.hello: %w", err)
	}
	if serverHelloEnv.Type != protocol.TypeServerHello {
		return fmt.Errorf("expected server.hello, got %s", serverHelloEnv.Type)
	}

	var serverHello protocol.ServerHelloPayload
	if err := json.Unmarshal(serverHelloEnv.Payload, &serverHello); err != nil {
		return fmt.Errorf("parsing server.hello: %w", err)
	}
	if !serverHello.Accepted {
		return fmt.Errorf("rejected by server: %s", serverHello.RejectReason)
	}
	slog.Info("authenticated", "server_version", serverHello.ServerVersion,
		"cluster_id", serverHello.ClusterID,
		"patroni_configured", serverHello.PatroniConfigured,
		"app_tier_always_available", serverHello.AppTierAlwaysAvailable,
	)
	if c.OnServerHello != nil {
		c.OnServerHello(serverHello)
	}
	if c.OnDisconnect != nil {
		defer c.OnDisconnect()
	}

	// Run pumps
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	errCh := make(chan error, 2)

	go func() { errCh <- c.writePump(connCtx, conn) }()
	go func() { errCh <- c.readPump(connCtx, conn) }()
	go c.heartbeatLoop(connCtx)

	select {
	case err := <-errCh:
		connCancel()
		return err
	case <-connCtx.Done():
		return nil
	}
}

func (c *Client) writePump(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case env := <-c.outbound:
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := wsjson.Write(writeCtx, conn, env)
			cancel()
			if err != nil {
				return fmt.Errorf("write error: %w", err)
			}
		}
	}
}

func (c *Client) readPump(ctx context.Context, conn *websocket.Conn) error {
	for {
		var env protocol.Envelope
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return fmt.Errorf("read error: %w", err)
		}
		if c.onMessage != nil {
			if err := c.onMessage(ctx, env); err != nil {
				slog.Error("message handler error", "error", err)
			}
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.HeartbeatFn == nil {
				continue
			}
			hb, err := c.HeartbeatFn()
			if err != nil {
				slog.Error("heartbeat metrics error", "error", err)
				continue
			}
			hb.NodeID = c.cfg.NodeID
			// The list and the send stay together, so a heartbeat queued after
			// a task's ack always lists it (see StartTask).
			c.tasksMu.Lock()
			hb.Tasks = slices.Collect(maps.Keys(c.tasks))
			payload, _ := json.Marshal(hb)
			c.Send(protocol.Envelope{
				ID:      newID(),
				Type:    protocol.TypeAgentHeartbeat,
				Payload: json.RawMessage(payload),
			})
			c.tasksMu.Unlock()
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:gosec
	return fmt.Sprintf("%x", b)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
