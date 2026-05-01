package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/google/uuid"
)

// patroniHTTPClient is the single shared HTTP client for all Patroni REST calls.
// Transport-level timeouts prevent hung connections; each call site owns its
// deadline via context so timeouts are tuned per operation.
var patroniHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	},
}

// patroniREST makes an authenticated request to the Patroni REST API on a given node.
func patroniREST(ctx context.Context, method, nodeIP, path, user, pass string, body []byte) ([]byte, int, error) {
	url := "http://" + nodeIP + ":8008" + path
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := patroniHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

// snapshotPatroniConfig fetches the current DCS config and stores it before a
// PATCH /config call. If the config is structurally identical to the most recent
// snapshot it is not written again. Failures are logged and do not block the caller.
func snapshotPatroniConfig(ctx context.Context, q *queries.PatroniSnapshotQuerier, clusterID uuid.UUID,
	primaryIP, restUser, restPass, source string) {
	snapshotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, status, err := patroniREST(snapshotCtx, http.MethodGet, primaryIP, "/config", restUser, restPass, nil)
	if err != nil || status != http.StatusOK {
		slog.Warn("patroni config snapshot: GET /config failed", "source", source, "status", status, "err", err)
		return
	}

	// Skip insert if structurally identical to the most recent snapshot.
	// Re-marshal both through map[string]any so key ordering is normalised.
	if existing, listErr := q.List(ctx, clusterID); listErr == nil && len(existing) > 0 {
		if canonicalJSON(body) == canonicalJSON(existing[0].Config) {
			return
		}
	}

	if err := q.Insert(ctx, clusterID, source, body); err != nil {
		slog.Warn("patroni config snapshot: insert failed", "source", source, "err", err)
		return
	}
	_ = q.Prune(ctx, clusterID)
}

// canonicalJSON unmarshals raw JSON into any and re-marshals it so that map keys
// are sorted and the result is comparable regardless of the original key order.
// Returns an empty string if the input is not valid JSON.
func canonicalJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
