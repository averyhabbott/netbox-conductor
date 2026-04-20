// Package syslog implements RFC 5424 syslog forwarding to remote destinations.
package syslog

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

var hostname string

func init() {
	h, err := os.Hostname()
	if err != nil {
		h = "netbox-conductor"
	}
	hostname = h
}

// severityCode maps our severity names to RFC 5424 severity values.
func severityCode(severity string) int {
	switch severity {
	case events.SeverityCritical:
		return 2 // Critical
	case events.SeverityError:
		return 3 // Error
	case events.SeverityWarn:
		return 4 // Warning
	case events.SeverityInfo:
		return 6 // Informational
	case events.SeverityDebug:
		return 7 // Debug
	default:
		return 6
	}
}

// Format returns an RFC 5424 syslog message for the given event.
// Facility 16 (local0) is used. The SD-ELEMENT carries structured event metadata.
func Format(ev events.Event) string {
	const facility = 16 // local0
	priority := facility*8 + severityCode(ev.Severity)
	ts := ev.OccurredAt.UTC().Format(time.RFC3339)

	clusterID := nilUUID(ev.ClusterID)
	nodeID := nilUUID(ev.NodeID)

	sd := fmt.Sprintf(
		`[netbox-conductor@32473 code=%q cluster_id=%q node_id=%q actor=%q]`,
		ev.Code, clusterID, nodeID, ev.Actor,
	)

	msg := strings.ReplaceAll(ev.Message, "\n", " ")
	return fmt.Sprintf("<%d>1 %s %s netbox-conductor - %s %s %s\n",
		priority, ts, hostname, ev.Code, sd, msg)
}

func nilUUID(id *uuid.UUID) string {
	if id == nil {
		return "-"
	}
	return id.String()
}
