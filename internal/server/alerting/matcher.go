// Package alerting implements the LibreNMS-style alert rule engine.
package alerting

import (
	"regexp"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// MatchesEvent returns true when the event satisfies all conditions in the rule.
// Empty slices / nil optional fields are treated as "match all".
func MatchesEvent(rule queries.AlertRule, ev events.Event) bool {
	// ── Category filter ───────────────────────────────────────────────────────
	if len(rule.Categories) > 0 {
		matched := false
		for _, c := range rule.Categories {
			if c == ev.Category {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// ── Code filter (exact or prefix) ─────────────────────────────────────────
	if len(rule.Codes) > 0 {
		matched := false
		for _, c := range rule.Codes {
			if strings.HasSuffix(c, "-") {
				if strings.HasPrefix(ev.Code, c) {
					matched = true
					break
				}
			} else {
				if ev.Code == c || strings.HasPrefix(ev.Code, c+"-") {
					// "NBC-HA" matches "NBC-HA-001" through "NBC-HA-999"
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}

	// ── Minimum severity ──────────────────────────────────────────────────────
	if rule.MinSeverity != "" {
		evRank := events.SeverityOrder[ev.Severity]
		ruleRank := events.SeverityOrder[rule.MinSeverity]
		if evRank < ruleRank {
			return false
		}
	}

	// ── Scope ─────────────────────────────────────────────────────────────────
	if rule.ClusterID != nil {
		if ev.ClusterID == nil || *ev.ClusterID != *rule.ClusterID {
			return false
		}
	}
	if rule.NodeID != nil {
		if ev.NodeID == nil || *ev.NodeID != *rule.NodeID {
			return false
		}
	}

	// ── Message regex ─────────────────────────────────────────────────────────
	if rule.MessageRegex != nil && *rule.MessageRegex != "" {
		re, err := regexp.Compile(*rule.MessageRegex)
		if err != nil || !re.MatchString(ev.Message) {
			return false
		}
	}

	return true
}

// MatchesMetric returns true when the latest heartbeat for a node satisfies the
// metric threshold condition in the rule.  Returns false if the rule has no
// metric condition or if hb is nil.
func MatchesMetric(rule queries.AlertRule, hb *queries.Heartbeat) bool {
	if rule.MetricField == nil || rule.MetricOperator == nil || rule.MetricValue == nil {
		return false
	}
	if hb == nil {
		return false
	}

	var val *float64
	switch *rule.MetricField {
	case "disk_used_pct":
		val = hb.DiskUsedPct
	case "load_avg_1":
		val = hb.LoadAvg1
	case "load_avg_5":
		val = hb.LoadAvg5
	case "mem_used_pct":
		val = hb.MemUsedPct
	case "replication_lag_bytes":
		if hb.ReplicationLagBytes != nil {
			f := float64(*hb.ReplicationLagBytes)
			val = &f
		}
	}
	if val == nil {
		return false
	}

	threshold := *rule.MetricValue
	switch *rule.MetricOperator {
	case ">":
		return *val > threshold
	case ">=":
		return *val >= threshold
	case "<":
		return *val < threshold
	case "<=":
		return *val <= threshold
	case "==":
		return *val == threshold
	}
	return false
}

// MatchesSchedule returns true when the current time falls within at least one
// of the rule's schedule windows (or when the rule has no schedule).
func MatchesSchedule(rule queries.AlertRule, schedule *queries.AlertSchedule, at time.Time) bool {
	if rule.ScheduleID == nil || schedule == nil {
		return true // no schedule restriction
	}
	loc := time.UTC
	if tz, err := time.LoadLocation(schedule.Timezone); err == nil {
		loc = tz
	}
	local := at.In(loc)
	dow := int(local.Weekday())
	hhmm := local.Format("15:04")

	for _, w := range schedule.Windows {
		dayMatch := false
		for _, d := range w.Days {
			if d == dow {
				dayMatch = true
				break
			}
		}
		if !dayMatch {
			continue
		}
		// Handle overnight windows (e.g. 22:00–02:00) where End < Start.
		if w.End > w.Start {
			if hhmm >= w.Start && hhmm < w.End {
				return true
			}
		} else {
			if hhmm >= w.Start || hhmm < w.End {
				return true
			}
		}
	}
	return false
}
