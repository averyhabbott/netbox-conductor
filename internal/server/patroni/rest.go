package patroni

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// httpClient is the single shared HTTP client for all Patroni REST calls.
// Transport-level timeouts prevent hung connections; each call site owns its
// deadline via context so timeouts are tuned per operation.
var httpClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	},
}

// defaultTimeout bounds a call whose context has no deadline.
const defaultTimeout = 10 * time.Second

// SwitchTimeout bounds POST /switchover and /failover: Patroni answers once
// the new leader holds the lock, waiting up to 2 × max(10s, loop_wait) = 20s
// with our loop_wait of 10.
const SwitchTimeout = 30 * time.Second

// REST makes an authenticated request to the Patroni REST API on a given node.
func REST(ctx context.Context, method, nodeIP, path, user, pass string, body []byte) ([]byte, int, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	url := "http://" + nodeIP + ":" + protocol.PatroniRESTPortStr + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}
