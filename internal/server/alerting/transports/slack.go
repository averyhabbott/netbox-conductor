package transports

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// SendSlackWebhook delivers an alert via a Slack incoming webhook URL.
// config keys: url
func SendSlackWebhook(cfg map[string]interface{}, rule queries.AlertRule, ev events.Event, isResolve bool) {
	url, _ := cfg["url"].(string)
	if url == "" {
		slog.Warn("alerting: slack_webhook transport missing 'url'", "rule", rule.Name)
		return
	}
	body := slackBody(rule, ev, isResolve)
	sendSlack(url, body, rule.Name)
}

// SendSlackBot delivers an alert via a Slack bot token to a specific channel.
// config keys: token_enc, channel
func SendSlackBot(cfg map[string]interface{}, rule queries.AlertRule, ev events.Event, isResolve bool) {
	token, _ := cfg["token_enc"].(string)
	channel, _ := cfg["channel"].(string)
	if token == "" || channel == "" {
		slog.Warn("alerting: slack_bot transport missing 'token' or 'channel'", "rule", rule.Name)
		return
	}
	body := slackBody(rule, ev, isResolve)
	body["channel"] = channel

	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", bytes.NewReader(bodyBytes))
	if err != nil {
		slog.Warn("alerting: slack bot request error", "rule", rule.Name, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("alerting: slack bot delivery failed", "rule", rule.Name, "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("alerting: slack bot message sent", "channel", channel, "rule", rule.Name)
}

func slackBody(rule queries.AlertRule, ev events.Event, isResolve bool) map[string]interface{} {
	emoji := severityEmoji(ev.Severity)
	if isResolve {
		emoji = ":white_check_mark:"
	}
	prefix := "ALERT"
	if isResolve {
		prefix = "RESOLVED"
	}

	text := fmt.Sprintf("%s *[%s] %s — %s*\n*Code:* `%s`\n*Message:* %s\n*Time:* %s",
		emoji, prefix, strings.ToUpper(ev.Severity), rule.Name,
		ev.Code, ev.Message,
		ev.OccurredAt.UTC().Format(time.RFC3339),
	)
	if ev.ClusterID != nil {
		text += fmt.Sprintf("\n*Cluster:* `%s`", ev.ClusterID)
	}
	if ev.NodeID != nil {
		text += fmt.Sprintf("\n*Node:* `%s`", ev.NodeID)
	}

	return map[string]interface{}{
		"text": text,
	}
}

func severityEmoji(severity string) string {
	switch severity {
	case "critical":
		return ":rotating_light:"
	case "error":
		return ":red_circle:"
	case "warn":
		return ":warning:"
	default:
		return ":information_source:"
	}
}

func sendSlack(url string, body map[string]interface{}, ruleName string) {
	bodyBytes, _ := json.Marshal(body)
	resp, err := client.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		slog.Warn("alerting: slack webhook delivery failed", "rule", ruleName, "error", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		slog.Warn("alerting: slack webhook returned error status", "rule", ruleName, "status", resp.StatusCode)
		return
	}
	slog.Info("alerting: slack webhook delivered", "rule", ruleName, "status", resp.StatusCode)
}

// TestSlackWebhook sends a test Slack notification.
func TestSlackWebhook(cfg map[string]interface{}) error {
	url, _ := cfg["url"].(string)
	if url == "" {
		return fmt.Errorf("url is required")
	}
	body := map[string]string{"text": ":white_check_mark: NetBox Conductor — test notification"}
	bodyBytes, _ := json.Marshal(body)
	resp, err := client.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return nil
}

// TestSlackBot sends a test Slack bot notification.
func TestSlackBot(cfg map[string]interface{}) error {
	token, _ := cfg["token_enc"].(string)
	channel, _ := cfg["channel"].(string)
	if token == "" || channel == "" {
		return fmt.Errorf("token and channel are required")
	}
	body, _ := json.Marshal(map[string]string{
		"channel": channel,
		"text":    ":white_check_mark: NetBox Conductor — test notification",
	})
	req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
