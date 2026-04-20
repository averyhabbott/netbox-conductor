package alerting

import (
	"log/slog"

	"github.com/averyhabbott/netbox-conductor/internal/server/alerting/transports"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// dispatch routes a fired alert to the correct transport implementation.
func dispatch(t queries.AlertTransport, rule queries.AlertRule, ev events.Event, isResolve bool) {
	switch t.Type {
	case "webhook":
		transports.SendWebhook(t.Config, rule, ev, isResolve)
	case "email":
		transports.SendEmail(t.Config, rule, ev, isResolve)
	case "slack_webhook":
		transports.SendSlackWebhook(t.Config, rule, ev, isResolve)
	case "slack_bot":
		transports.SendSlackBot(t.Config, rule, ev, isResolve)
	default:
		slog.Warn("alerting: unknown transport type", "type", t.Type, "rule", rule.Name)
	}
}
