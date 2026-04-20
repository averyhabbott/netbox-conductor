// Package transports implements alert delivery channels.
package transports

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"text/template"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// defaultWebhookTemplate is used when no custom body_template is configured.
const defaultWebhookTemplate = `{
  "event": "alert.fired",
  "rule": "{{.RuleName}}",
  "severity": "{{.Severity}}",
  "code": "{{.Code}}",
  "message": {{jsonStr .Message}},
  "actor": "{{.Actor}}",
  "cluster_id": "{{.ClusterID}}",
  "node_id": "{{.NodeID}}",
  "fired_at": "{{.FiredAt}}"
}`

// WebhookPayload is passed to the body template.
type WebhookPayload struct {
	RuleName  string
	Severity  string
	Code      string
	Message   string
	Actor     string
	ClusterID string
	NodeID    string
	FiredAt   string
	IsResolve bool
}

var client = &http.Client{Timeout: 10 * time.Second}

// SendWebhook delivers an alert to a webhook URL.
func SendWebhook(cfg map[string]interface{}, rule queries.AlertRule, ev events.Event, isResolve bool) {
	rawURL, _ := cfg["url"].(string)
	if rawURL == "" {
		return
	}
	method := "POST"
	if m, ok := cfg["method"].(string); ok && m != "" {
		method = m
	}

	payload := buildPayload(rule, ev, isResolve)

	var bodyBytes []byte
	if tmplStr, ok := cfg["body_template"].(string); ok && tmplStr != "" {
		bodyBytes = renderTemplate(tmplStr, payload)
	} else {
		bodyBytes = renderTemplate(defaultWebhookTemplate, payload)
	}

	req, err := http.NewRequest(method, rawURL, bytes.NewReader(bodyBytes))
	if err != nil {
		slog.Warn("alerting: webhook request error", "url", rawURL, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if hdrs, ok := cfg["headers"].(map[string]interface{}); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				req.Header.Set(k, s)
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("alerting: webhook delivery failed", "url", rawURL, "error", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		slog.Warn("alerting: webhook returned error status", "url", rawURL, "status", resp.StatusCode)
		return
	}
	slog.Info("alerting: webhook delivered", "url", rawURL, "status", resp.StatusCode)
}

func buildPayload(rule queries.AlertRule, ev events.Event, isResolve bool) WebhookPayload {
	clusterID := ""
	if ev.ClusterID != nil {
		clusterID = ev.ClusterID.String()
	}
	nodeID := ""
	if ev.NodeID != nil {
		nodeID = ev.NodeID.String()
	}
	return WebhookPayload{
		RuleName:  rule.Name,
		Severity:  ev.Severity,
		Code:      ev.Code,
		Message:   ev.Message,
		Actor:     ev.Actor,
		ClusterID: clusterID,
		NodeID:    nodeID,
		FiredAt:   ev.OccurredAt.UTC().Format(time.RFC3339),
		IsResolve: isResolve,
	}
}

func renderTemplate(tmplStr string, data WebhookPayload) []byte {
	funcMap := template.FuncMap{
		"jsonStr": func(s string) string {
			b, _ := json.Marshal(s)
			return string(b)
		},
	}
	t, err := template.New("body").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		// Fall back to plain JSON on parse error.
		b, _ := json.Marshal(data)
		return b
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		b, _ := json.Marshal(data)
		return b
	}
	return buf.Bytes()
}

// Test sends a test webhook payload.
func TestWebhook(cfg map[string]interface{}) error {
	rawURL, _ := cfg["url"].(string)
	if rawURL == "" {
		return fmt.Errorf("url is required")
	}
	body, _ := json.Marshal(map[string]string{
		"event":   "alert.test",
		"message": "This is a test notification from NetBox Conductor",
	})
	resp, err := client.Post(rawURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
