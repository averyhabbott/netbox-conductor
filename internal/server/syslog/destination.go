package syslog

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
)

// Destination wraps a SyslogDestination row and manages the network connection.
type Destination struct {
	cfg queries.SyslogDestination

	mu   sync.Mutex
	conn net.Conn
}

func NewDestination(cfg queries.SyslogDestination) *Destination {
	return &Destination{cfg: cfg}
}

// Send writes a formatted syslog message to the destination.
// For UDP a new socket is created per message; TCP/TLS connections are reused.
func (d *Destination) Send(msg string) {
	addr := fmt.Sprintf("%s:%d", d.cfg.Host, d.cfg.Port)
	switch d.cfg.Protocol {
	case "udp":
		d.sendUDP(addr, msg)
	case "tcp":
		d.sendTCP(addr, msg, nil)
	case "tcp+tls":
		tlsCfg := d.tlsConfig()
		d.sendTCP(addr, msg, tlsCfg)
	}
}

func (d *Destination) sendUDP(addr, msg string) {
	conn, err := net.DialTimeout("udp", addr, 5*time.Second)
	if err != nil {
		slog.Warn("syslog: udp dial failed", "addr", addr, "error", err)
		return
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprint(conn, msg); err != nil {
		slog.Warn("syslog: udp write failed", "addr", addr, "error", err)
	}
}

func (d *Destination) sendTCP(addr, msg string, tlsCfg *tls.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.conn == nil {
		if err := d.connect(addr, tlsCfg); err != nil {
			slog.Warn("syslog: tcp connect failed", "addr", addr, "error", err)
			return
		}
	}

	_ = d.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprint(d.conn, msg); err != nil {
		slog.Warn("syslog: tcp write failed, reconnecting", "addr", addr, "error", err)
		d.conn.Close()
		d.conn = nil
		if err := d.connect(addr, tlsCfg); err != nil {
			slog.Warn("syslog: tcp reconnect failed", "addr", addr, "error", err)
			return
		}
		_ = d.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprint(d.conn, msg); err != nil {
			slog.Warn("syslog: tcp write after reconnect failed", "addr", addr, "error", err)
		}
	}
}

func (d *Destination) connect(addr string, tlsCfg *tls.Config) error {
	if tlsCfg != nil {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, tlsCfg)
		if err != nil {
			return err
		}
		d.conn = conn
	} else {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			return err
		}
		d.conn = conn
	}
	return nil
}

func (d *Destination) tlsConfig() *tls.Config {
	cfg := &tls.Config{ServerName: d.cfg.Host}
	if d.cfg.TLSCACert != nil && *d.cfg.TLSCACert != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(*d.cfg.TLSCACert)) {
			cfg.RootCAs = pool
		}
	}
	return cfg
}

// Close tears down the underlying TCP connection if open.
func (d *Destination) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
}
