package transports

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// SendEmail delivers an alert via SMTP using configuration stored in the DB.
// Config keys: smtp_host, smtp_port, smtp_tls (bool), smtp_user, smtp_pass_enc, smtp_from, to (string or array).
func SendEmail(cfg map[string]interface{}, rule queries.AlertRule, ev events.Event, isResolve bool) {
	host, port, from, recipients, user, pass, useTLS, err := parseEmailCfg(cfg)
	if err != nil {
		slog.Warn("alerting: email transport config error", "rule", rule.Name, "error", err)
		return
	}

	prefix := "[ALERT]"
	if isResolve {
		prefix = "[RESOLVED]"
	}
	subject := fmt.Sprintf("%s NetBox Conductor — %s: %s", prefix, strings.ToUpper(ev.Severity), rule.Name)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Rule:     %s\n", rule.Name))
	sb.WriteString(fmt.Sprintf("Code:     %s\n", ev.Code))
	sb.WriteString(fmt.Sprintf("Severity: %s\n", strings.ToUpper(ev.Severity)))
	sb.WriteString(fmt.Sprintf("Message:  %s\n", ev.Message))
	sb.WriteString(fmt.Sprintf("Actor:    %s\n", ev.Actor))
	sb.WriteString(fmt.Sprintf("Time:     %s\n", ev.OccurredAt.UTC().Format(time.RFC3339)))
	if ev.ClusterID != nil {
		sb.WriteString(fmt.Sprintf("Cluster:  %s\n", ev.ClusterID))
	}
	if ev.NodeID != nil {
		sb.WriteString(fmt.Sprintf("Node:     %s\n", ev.NodeID))
	}
	if isResolve {
		sb.WriteString("\nThis alert has been resolved.\n")
	}

	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		from, strings.Join(recipients, ", "), subject, sb.String()))

	if err := sendMail(host, port, from, recipients, msg, user, pass, useTLS); err != nil {
		slog.Warn("alerting: email delivery failed", "to", recipients, "rule", rule.Name, "error", err)
		return
	}
	slog.Info("alerting: email delivered", "to", recipients, "rule", rule.Name)
}

// TestEmail sends a test email using the given config.
func TestEmail(cfg map[string]interface{}) error {
	host, port, from, recipients, user, pass, useTLS, err := parseEmailCfg(cfg)
	if err != nil {
		return err
	}
	msg := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: NetBox Conductor — Test Notification\r\n\r\nThis is a test notification from NetBox Conductor.",
		from, strings.Join(recipients, ", ")))
	return sendMail(host, port, from, recipients, msg, user, pass, useTLS)
}

// parseEmailCfg extracts and validates SMTP config from the transport config map.
func parseEmailCfg(cfg map[string]interface{}) (host, port, from string, recipients []string, user, pass string, useTLS bool, err error) {
	host, _ = cfg["smtp_host"].(string)
	if host == "" {
		host = "localhost"
	}

	// smtp_port may be stored as float64 (JSON number) or string.
	switch v := cfg["smtp_port"].(type) {
	case float64:
		port = fmt.Sprintf("%d", int(v))
	case string:
		port = v
	}
	if port == "" {
		port = "25"
	}

	from, _ = cfg["smtp_from"].(string)
	if from == "" {
		from = "netbox-conductor@localhost"
	}

	recipients = extractRecipients(cfg)
	if len(recipients) == 0 {
		err = fmt.Errorf("'to' address is required")
		return
	}

	user, _ = cfg["smtp_user"].(string)
	pass, _ = cfg["smtp_pass_enc"].(string)
	useTLS, _ = cfg["smtp_tls"].(bool)
	return
}

// extractRecipients handles both a JSON string and a JSON array for the "to" field.
func extractRecipients(cfg map[string]interface{}) []string {
	switch v := cfg["to"].(type) {
	case string:
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []interface{}:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// sendMail sends an email. If useTLS is true it uses implicit TLS (SMTPS / port 465);
// otherwise it uses smtp.SendMail which negotiates STARTTLS when the server offers it.
func sendMail(host, port, from string, recipients []string, body []byte, user, pass string, useTLS bool) error {
	addr := host + ":" + port

	if useTLS {
		tlsCfg := &tls.Config{ServerName: host}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("tls dial: %w", err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
		defer c.Close()
		if user != "" && pass != "" {
			if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		return sendViaClient(c, from, recipients, body)
	}

	var auth smtp.Auth
	if user != "" && pass != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	return smtp.SendMail(addr, auth, from, recipients, body)
}

// sendViaClient sends a message using an already-connected smtp.Client.
func sendViaClient(c *smtp.Client, from string, recipients []string, body []byte) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range recipients {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return w.Close()
}
