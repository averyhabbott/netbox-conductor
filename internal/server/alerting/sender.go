// Package alerting delivers alert notifications via webhook or email.
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/google/uuid"
)

// Sender fires and resolves alerts, storing them in the database and
// delivering notifications to configured destinations.
type Sender struct {
	alertQ  *queries.AlertQuerier
	httpCli *http.Client
}

// New creates a Sender backed by the given AlertQuerier.
func New(alertQ *queries.AlertQuerier) *Sender {
	return &Sender{
		alertQ:  alertQ,
		httpCli: &http.Client{Timeout: 10 * time.Second},
	}
}

// Condition constants understood by alert configs.
const (
	CondAgentDisconnected = "agent_disconnected"
	CondNetboxDown        = "netbox_down"
	CondRQDown            = "rq_down"
)

// FireAgentDisconnect records and delivers an alert when an agent disconnects.
// It is safe to call from a goroutine.
func (s *Sender) FireAgentDisconnect(nodeID, clusterID uuid.UUID, hostname string) {
	ctx := context.Background()
	nid := nodeID
	cid := clusterID
	alert, err := s.alertQ.FireAlert(ctx, &cid, &nid,
		"warn", CondAgentDisconnected,
		fmt.Sprintf("Agent on %s has disconnected", hostname),
	)
	if err != nil {
		slog.Warn("alerting: failed to store agent_disconnected alert", "node", nodeID, "error", err)
		return
	}
	configs, err := s.alertQ.EnabledConfigsForCondition(ctx, CondAgentDisconnected)
	if err != nil {
		slog.Warn("alerting: failed to load configs for agent_disconnected", "error", err)
		return
	}
	s.dispatch(configs, alert)
}

// ResolveAgentDisconnect clears the disconnected alert when the agent reconnects.
func (s *Sender) ResolveAgentDisconnect(nodeID, clusterID uuid.UUID) {
	ctx := context.Background()
	nid := nodeID
	cid := clusterID
	if err := s.alertQ.ResolveAlert(ctx, &cid, &nid, CondAgentDisconnected); err != nil {
		slog.Warn("alerting: failed to resolve agent_disconnected alert", "node", nodeID, "error", err)
	}
}

// dispatch sends the alert payload to every provided config destination.
func (s *Sender) dispatch(configs []queries.AlertConfig, alert *queries.ActiveAlert) {
	for _, cfg := range configs {
		switch cfg.Type {
		case "webhook":
			if cfg.WebhookURL != nil && *cfg.WebhookURL != "" {
				go s.sendWebhook(*cfg.WebhookURL, alert)
			}
		case "email":
			if cfg.EmailTo != nil && *cfg.EmailTo != "" {
				go s.sendEmail(*cfg.EmailTo, alert)
			}
		}
	}
}

// webhookPayload is the JSON body posted to a webhook destination.
type webhookPayload struct {
	Event     string    `json:"event"`
	Severity  string    `json:"severity"`
	Condition string    `json:"condition"`
	Message   string    `json:"message"`
	FiredAt   time.Time `json:"fired_at"`
	ClusterID string    `json:"cluster_id,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
}

func (s *Sender) sendWebhook(url string, alert *queries.ActiveAlert) {
	p := webhookPayload{
		Event:     "alert.fired",
		Severity:  alert.Severity,
		Condition: alert.Condition,
		Message:   alert.Message,
		FiredAt:   alert.FiredAt,
	}
	if alert.ClusterID != nil {
		p.ClusterID = alert.ClusterID.String()
	}
	if alert.NodeID != nil {
		p.NodeID = alert.NodeID.String()
	}
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	resp, err := s.httpCli.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("alerting: webhook delivery failed", "url", url, "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("alerting: webhook delivered", "url", url, "status", resp.StatusCode)
}

// sendEmail delivers a plain-text alert email.
// SMTP settings are read from environment variables:
//
//	SMTP_HOST (default: localhost), SMTP_PORT (default: 25)
//	SMTP_USER, SMTP_PASS, SMTP_FROM
func (s *Sender) sendEmail(to string, alert *queries.ActiveAlert) {
	host := envOr("SMTP_HOST", "localhost")
	port := envOr("SMTP_PORT", "25")
	from := envOr("SMTP_FROM", "netbox-conductor@localhost")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")

	recipients := strings.Split(to, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	subject := fmt.Sprintf("[NetBox Conductor] %s: %s", strings.ToUpper(alert.Severity), alert.Condition)
	body := fmt.Sprintf("Condition: %s\nSeverity:  %s\nMessage:   %s\nFired at:  %s\n",
		alert.Condition, alert.Severity, alert.Message, alert.FiredAt.UTC().Format(time.RFC3339))
	if alert.ClusterID != nil {
		body += fmt.Sprintf("Cluster:   %s\n", alert.ClusterID)
	}
	if alert.NodeID != nil {
		body += fmt.Sprintf("Node:      %s\n", alert.NodeID)
	}

	msg := []byte("From: " + from + "\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" +
		body)

	addr := host + ":" + port
	var auth smtp.Auth
	if user != "" && pass != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	if err := smtp.SendMail(addr, auth, from, recipients, msg); err != nil {
		slog.Warn("alerting: email delivery failed", "to", to, "error", err)
		return
	}
	slog.Info("alerting: email delivered", "to", to, "condition", alert.Condition)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
