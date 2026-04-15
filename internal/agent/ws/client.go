// Package ws manages the agent's outbound WebSocket connection to the tool server.
package ws

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/abottVU/netbox-failover/internal/agent/config"
	"github.com/abottVU/netbox-failover/internal/shared/protocol"
)

const (
	minBackoff     = 5 * time.Second
	maxBackoff     = 120 * time.Second
	heartbeatEvery = 15 * time.Second
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
}

// New creates a Client. HeartbeatFn must be set before Run is called.
func New(cfg *config.Config, onMessage MessageHandler) (*Client, error) {
	httpClient, err := buildHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("building HTTP client: %w", err)
	}
	return &Client{
		cfg:        cfg,
		onMessage:  onMessage,
		httpClient: httpClient,
		outbound:   make(chan protocol.Envelope, 64),
	}, nil
}

// Send enqueues an envelope for delivery to the server.
func (c *Client) Send(env protocol.Envelope) {
	select {
	case c.outbound <- env:
	default:
		log.Println("outbound buffer full, dropping message")
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
			log.Printf("connection error: %v — reconnecting in %s", err, backoff)
		} else {
			log.Println("disconnected — reconnecting...")
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

	log.Printf("connected to %s", c.cfg.ServerURL)

	// Send agent.hello
	hostname, _ := os.Hostname()
	helloPayload, _ := json.Marshal(protocol.AgentHelloPayload{
		NodeID:       c.cfg.NodeID,
		Token:        c.cfg.Token,
		AgentVersion: agentVersion,
		Hostname:     hostname,
		OS:           "linux",
		Arch:         "amd64",
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
	log.Printf("authenticated (server version %s)", serverHello.ServerVersion)

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
				log.Printf("message handler error: %v", err)
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
				log.Printf("heartbeat metrics error: %v", err)
				continue
			}
			hb.NodeID = c.cfg.NodeID
			payload, _ := json.Marshal(hb)
			c.Send(protocol.Envelope{
				ID:      newID(),
				Type:    protocol.TypeAgentHeartbeat,
				Payload: json.RawMessage(payload),
			})
		}
	}
}

func buildHTTPClient(cfg *config.Config) (*http.Client, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // intentional dev option, warned at startup
	}

	if cfg.TLSCACert != "" {
		pemData, err := os.ReadFile(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("reading CA cert %s: %w", cfg.TLSCACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("no valid certs in %s", cfg.TLSCACert)
		}
		tlsCfg.RootCAs = pool
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}, nil
}

func newID() string {
	// Simple random hex ID — good enough for correlation
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

// isWS returns true if the URL uses the ws:// scheme.
func isWS(url string) bool {
	return strings.HasPrefix(url, "ws://")
}

var _ = isWS // suppress unused warning
