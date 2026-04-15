package hub

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// Session represents a single authenticated agent WebSocket connection.
type Session struct {
	NodeID          uuid.UUID
	ClusterID       uuid.UUID
	AgentVersion    string // from agent.hello
	NetboxVersion   string // from heartbeat; updated on each heartbeat
	// NetboxLogPathFn derives the on-disk path for a forwarded NetBox log file.
	// logFilename is the base name as reported by the agent, e.g. "netbox.log".
	// Nil when the node was connected before log-forwarding was configured.
	NetboxLogPathFn func(logFilename string) string
	conn            *websocket.Conn

	// send is a buffered channel of outbound envelopes.
	// The write pump drains it; callers should never block on it.
	send chan protocol.Envelope

	mu          sync.Mutex
	connectedAt time.Time
	lastSeen    time.Time
}

// NewSession creates a new authenticated agent session.
func NewSession(nodeID, clusterID uuid.UUID, conn *websocket.Conn) *Session {
	return &Session{
		NodeID:      nodeID,
		ClusterID:   clusterID,
		conn:        conn,
		send:        make(chan protocol.Envelope, 64),
		connectedAt: time.Now(),
		lastSeen:    time.Now(),
	}
}

// Send enqueues an envelope for delivery to the agent.
// Returns false if the send buffer is full (agent is not keeping up).
func (s *Session) Send(env protocol.Envelope) bool {
	select {
	case s.send <- env:
		return true
	default:
		return false
	}
}

// TouchLastSeen updates the last-seen timestamp (called on heartbeat receipt).
func (s *Session) TouchLastSeen() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// LastSeen returns the most recent heartbeat time.
func (s *Session) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// Close forces the underlying WebSocket connection closed without a close
// handshake. Called by Hub.Register when a new connection for the same node
// evicts this session, so readPump exits promptly instead of blocking.
func (s *Session) Close() {
	s.conn.CloseNow()
}

// WritePump drains the send channel and writes to the WebSocket.
// Runs in its own goroutine; exits when ctx is cancelled or send is closed.
func (s *Session) WritePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-s.send:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = wsjson.Write(writeCtx, s.conn, env)
			cancel()
		}
	}
}
