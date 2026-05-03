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

// snapshotPatroniConfig fetches the current DCS config and stores it as a
// pre-change snapshot. If the config is structurally identical to the most
// recent snapshot it is not written again (dedup). If it differs from the
// most recent snapshot and the source indicates an external change, it is
// recorded as "discovered". Failures are logged and do not block the caller.
func snapshotPatroniConfig(ctx context.Context, q *queries.PatroniSnapshotQuerier, clusterID uuid.UUID,
	primaryIP, restUser, restPass, source string) {
	snapshotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, status, err := patroniREST(snapshotCtx, http.MethodGet, primaryIP, "/config", restUser, restPass, nil)
	if err != nil || status != http.StatusOK {
		slog.Warn("patroni config snapshot: GET /config failed", "source", source, "status", status, "err", err)
		return
	}

	existing, listErr := q.List(ctx, clusterID)
	if listErr == nil && len(existing) > 0 {
		// Find the active snapshot — it may not be existing[0] if SetActive moved
		// the active flag to an older record.
		var activeSnap *queries.PatroniConfigSnapshot
		for i := range existing {
			if existing[i].IsActive {
				activeSnap = &existing[i]
				break
			}
		}
		if activeSnap != nil {
			if canonicalJSON(body) == canonicalJSON(activeSnap.Config) {
				// Live matches active — no pre-change snapshot needed.
				return
			}
			// Live differs from active — config drifted outside conductor.
			if err := q.Insert(ctx, clusterID, "discovered", body); err != nil {
				slog.Warn("patroni config snapshot: insert discovered failed", "err", err)
			}
			_ = q.Prune(ctx, clusterID)
			return
		}
		// No active snapshot — dedup against most recent.
		if canonicalJSON(body) == canonicalJSON(existing[0].Config) {
			return
		}
	}

	// No existing snapshots — record as the provided source
	if err := q.Insert(ctx, clusterID, source, body); err != nil {
		slog.Warn("patroni config snapshot: insert failed", "source", source, "err", err)
		return
	}
	_ = q.Prune(ctx, clusterID)
}

// recordPostChangeSnapshot fetches the current DCS config after a successful
// PATCH and stores it as the new active snapshot. Failures are logged only.
func recordPostChangeSnapshot(ctx context.Context, q *queries.PatroniSnapshotQuerier, clusterID uuid.UUID,
	primaryIP, restUser, restPass, source string) {
	snapshotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, status, err := patroniREST(snapshotCtx, http.MethodGet, primaryIP, "/config", restUser, restPass, nil)
	if err != nil || status != http.StatusOK {
		slog.Warn("patroni post-change snapshot: GET /config failed", "source", source, "status", status, "err", err)
		return
	}
	if err := q.InsertActive(ctx, clusterID, source, body); err != nil {
		slog.Warn("patroni post-change snapshot: insert failed", "source", source, "err", err)
		return
	}
	_ = q.Prune(ctx, clusterID)
}

// setActiveForDesigned records the post-revert state as a "user-revert" snapshot.
// Exception: if the most recent snapshot is already tagged "user-revert" and its
// config matches intendedJSON, just mark it active — no duplicate needed.
func setActiveForDesigned(ctx context.Context, q *queries.PatroniSnapshotQuerier, clusterID uuid.UUID,
	intendedJSON []byte, primaryIP, restUser, restPass string) {
	existing, err := q.List(ctx, clusterID)
	if err == nil && len(existing) > 0 {
		most := existing[0]
		if most.Source == "user-revert" && canonicalJSON(most.Config) == canonicalJSON(intendedJSON) {
			if err := q.SetActive(ctx, most.ID, clusterID); err != nil {
				slog.Warn("patroni revert: SetActive failed", "snapshot", most.ID, "err", err)
			}
			_ = q.Prune(ctx, clusterID)
			return
		}
	}
	recordPostChangeSnapshot(ctx, q, clusterID, primaryIP, restUser, restPass, "user-revert")
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

// diffPatroniConfig computes the PATCH body needed to transition from current
// to intended: keys with changed values get their intended value, keys present
// in current but absent in intended are set to null (Patroni treats null as delete).
// Returns nil if the configs are identical (no PATCH needed).
func diffPatroniConfig(current, intended map[string]any) map[string]any {
	patch := make(map[string]any)
	diffObjects(current, intended, patch)
	if len(patch) == 0 {
		return nil
	}
	return patch
}

func diffObjects(current, intended map[string]any, patch map[string]any) {
	// Keys in intended: set if different from current
	for k, iv := range intended {
		cv, exists := current[k]
		if !exists {
			patch[k] = iv
			continue
		}
		iMap, iIsMap := iv.(map[string]any)
		cMap, cIsMap := cv.(map[string]any)
		if iIsMap && cIsMap {
			sub := make(map[string]any)
			diffObjects(cMap, iMap, sub)
			if len(sub) > 0 {
				patch[k] = sub
			}
		} else if canonicalJSON(mustMarshal(iv)) != canonicalJSON(mustMarshal(cv)) {
			patch[k] = iv
		}
	}
	// Keys in current but not in intended: null them out
	for k := range current {
		if _, inIntended := intended[k]; !inIntended {
			patch[k] = nil
		}
	}
}

