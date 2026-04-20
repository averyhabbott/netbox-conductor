package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// Engine evaluates alert rules against the incoming event stream and manages
// the alert state machine (fire_once, re_alert, every_occurrence, escalation).
// It implements events.Sink.
type Engine struct {
	mu        sync.RWMutex
	rules     []queries.AlertRule
	schedules map[uuid.UUID]*queries.AlertSchedule

	ruleQ   *queries.AlertRuleQuerier
	stateQ  *queries.AlertStateQuerier
	transQ  *queries.AlertTransportQuerier
	schedQ  *queries.AlertScheduleQuerier
	hbQ     *queries.HeartbeatQuerier

	eventCh chan events.Event
}

// NewEngine creates an Engine. Call Start to begin processing.
func NewEngine(
	ruleQ *queries.AlertRuleQuerier,
	stateQ *queries.AlertStateQuerier,
	transQ *queries.AlertTransportQuerier,
	schedQ *queries.AlertScheduleQuerier,
	hbQ *queries.HeartbeatQuerier,
) *Engine {
	return &Engine{
		schedules: make(map[uuid.UUID]*queries.AlertSchedule),
		ruleQ:     ruleQ,
		stateQ:    stateQ,
		transQ:    transQ,
		schedQ:    schedQ,
		hbQ:       hbQ,
		eventCh:   make(chan events.Event, 512),
	}
}

// OnEvent implements events.Sink. Events are enqueued for async processing.
func (e *Engine) OnEvent(ev events.Event) {
	select {
	case e.eventCh <- ev:
	default:
		slog.Warn("alerting: event channel full, dropping event", "code", ev.Code)
	}
}

// Start loads rules and launches background goroutines. ctx controls lifecycle.
func (e *Engine) Start(ctx context.Context) {
	e.reloadRules(ctx)
	go e.eventLoop(ctx)
	go e.reloadLoop(ctx)
	go e.timerLoop(ctx)
}

// ─── background loops ─────────────────────────────────────────────────────────

func (e *Engine) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.eventCh:
			e.processEvent(ctx, ev)
		}
	}
}

func (e *Engine) reloadLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.reloadRules(ctx)
		}
	}
}

func (e *Engine) timerLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.processTimers(ctx)
			e.processMetrics(ctx)
		}
	}
}

// ─── rule management ─────────────────────────────────────────────────────────

func (e *Engine) reloadRules(ctx context.Context) {
	rules, err := e.ruleQ.ListEnabled(ctx)
	if err != nil {
		slog.Warn("alerting: failed to reload rules", "error", err)
		return
	}

	scheduleIDs := make(map[uuid.UUID]struct{})
	for _, r := range rules {
		if r.ScheduleID != nil {
			scheduleIDs[*r.ScheduleID] = struct{}{}
		}
	}
	schedules := make(map[uuid.UUID]*queries.AlertSchedule, len(scheduleIDs))
	for id := range scheduleIDs {
		s, err := e.schedQ.GetByID(ctx, id)
		if err != nil {
			slog.Warn("alerting: failed to load schedule", "id", id, "error", err)
			continue
		}
		schedules[id] = s
	}

	e.mu.Lock()
	e.rules = rules
	e.schedules = schedules
	e.mu.Unlock()
	slog.Debug("alerting: rules reloaded", "count", len(rules))
}

// ─── event processing ─────────────────────────────────────────────────────────

func (e *Engine) processEvent(ctx context.Context, ev events.Event) {
	e.mu.RLock()
	rules := e.rules
	schedules := e.schedules
	e.mu.RUnlock()

	now := time.Now().UTC()
	for _, rule := range rules {
		if !MatchesEvent(rule, ev) {
			continue
		}
		var sched *queries.AlertSchedule
		if rule.ScheduleID != nil {
			sched = schedules[*rule.ScheduleID]
		}
		if !MatchesSchedule(rule, sched, now) {
			continue
		}
		e.fireAlert(ctx, rule, ev, now, false)
	}
}

func (e *Engine) fireAlert(ctx context.Context, rule queries.AlertRule, ev events.Event, now time.Time, isResolve bool) {
	if rule.FireMode == "every_occurrence" {
		e.dispatchToTransports(ctx, rule, ev, isResolve)
		return
	}

	state, isNew, err := e.stateQ.UpsertActive(ctx, rule.ID, ev.ClusterID, ev.NodeID)
	if err != nil {
		slog.Warn("alerting: upsert alert state failed", "rule", rule.Name, "error", err)
		return
	}

	if isNew {
		e.dispatchToTransports(ctx, rule, ev, false)
		_ = e.stateQ.MarkAlerted(ctx, state.ID)
		return
	}

	// fire_once: already fired, do nothing until resolved
	if rule.FireMode == "once" {
		return
	}

	// re_alert: handled by the timer loop
}

// ─── timer processing (re-alerts and escalation) ─────────────────────────────

func (e *Engine) processTimers(ctx context.Context) {
	states, err := e.stateQ.ListActive(ctx)
	if err != nil {
		slog.Warn("alerting: list active states failed", "error", err)
		return
	}

	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	ruleMap := make(map[uuid.UUID]queries.AlertRule, len(rules))
	for _, r := range rules {
		ruleMap[r.ID] = r
	}

	now := time.Now().UTC()
	for _, state := range states {
		rule, ok := ruleMap[state.RuleID]
		if !ok {
			continue
		}

		// Re-alert
		if rule.FireMode == "re_alert" && rule.ReAlertMins != nil && state.LastAlertedAt != nil {
			interval := time.Duration(*rule.ReAlertMins) * time.Minute
			maxOK := rule.MaxReAlerts == nil || state.ReAlertCount < *rule.MaxReAlerts
			if maxOK && now.After(state.LastAlertedAt.Add(interval)) {
				ev := e.syntheticEvent(rule, state,
					fmt.Sprintf("Re-alert #%d: rule %q still active", state.ReAlertCount+1, rule.Name))
				e.dispatchToTransports(ctx, rule, ev, false)
				_ = e.stateQ.MarkAlerted(ctx, state.ID)
			}
		}

		// Escalation
		if !state.Escalated && rule.EscalateAfterMins != nil && rule.EscalateTransportID != nil {
			after := time.Duration(*rule.EscalateAfterMins) * time.Minute
			if now.After(state.FirstFiredAt.Add(after)) {
				t, err := e.transQ.GetByID(ctx, *rule.EscalateTransportID)
				if err == nil && t.Enabled {
					ev := e.syntheticEvent(rule, state,
						fmt.Sprintf("Escalation: rule %q has been active for %d minutes", rule.Name, *rule.EscalateAfterMins))
					go dispatch(*t, rule, ev, false)
					_ = e.stateQ.MarkEscalated(ctx, state.ID)
				}
			}
		}
	}
}

// ─── metric-based rules ───────────────────────────────────────────────────────

func (e *Engine) processMetrics(ctx context.Context) {
	e.mu.RLock()
	rules := e.rules
	schedules := e.schedules
	e.mu.RUnlock()

	now := time.Now().UTC()
	for _, rule := range rules {
		if rule.MetricField == nil || rule.NodeID == nil {
			continue
		}
		var sched *queries.AlertSchedule
		if rule.ScheduleID != nil {
			sched = schedules[*rule.ScheduleID]
		}
		if !MatchesSchedule(rule, sched, now) {
			continue
		}
		hb, err := e.hbQ.LastByNode(ctx, *rule.NodeID)
		if err != nil || hb == nil {
			continue
		}
		clusterID := hb.ClusterID
		nodeID := hb.NodeID
		if MatchesMetric(rule, hb) {
			ev := events.Event{
				ID:        uuid.New(),
				ClusterID: &clusterID,
				NodeID:    &nodeID,
				Category:  events.CategoryService,
				Severity:  rule.MinSeverity,
				Code:      "metric:" + *rule.MetricField,
				Message:   fmt.Sprintf("Metric alert: %s %s %g", *rule.MetricField, *rule.MetricOperator, *rule.MetricValue),
				Actor:     events.ActorSystem,
				OccurredAt: now,
			}
			e.fireAlert(ctx, rule, ev, now, false)
		} else {
			// Metric condition no longer holds — auto-resolve
			resolved, err := e.stateQ.Resolve(ctx, rule.ID, &clusterID, &nodeID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("alerting: metric resolve failed", "rule", rule.Name, "error", err)
				continue
			}
			if resolved != nil && rule.NotifyOnClear {
				ev := events.Event{
					ID:        uuid.New(),
					ClusterID: &clusterID,
					NodeID:    &nodeID,
					Category:  events.CategoryService,
					Severity:  rule.MinSeverity,
					Code:      "metric:" + *rule.MetricField,
					Message:   fmt.Sprintf("Resolved: %s is back within threshold", *rule.MetricField),
					Actor:     events.ActorSystem,
					OccurredAt: now,
				}
				e.dispatchToTransports(ctx, rule, ev, true)
			}
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func (e *Engine) dispatchToTransports(ctx context.Context, rule queries.AlertRule, ev events.Event, isResolve bool) {
	for _, tid := range rule.TransportIDs {
		t, err := e.transQ.GetByID(ctx, tid)
		if err != nil {
			slog.Warn("alerting: failed to load transport", "id", tid, "rule", rule.Name, "error", err)
			continue
		}
		if !t.Enabled {
			continue
		}
		go dispatch(*t, rule, ev, isResolve)
	}
}

func (e *Engine) syntheticEvent(rule queries.AlertRule, state queries.AlertState, message string) events.Event {
	severity := rule.MinSeverity
	if severity == "" {
		severity = events.SeverityInfo
	}
	code := ""
	if len(rule.Codes) > 0 {
		code = rule.Codes[0]
	}
	category := ""
	if len(rule.Categories) > 0 {
		category = rule.Categories[0]
	}
	return events.Event{
		ID:         uuid.New(),
		ClusterID:  state.ClusterID,
		NodeID:     state.NodeID,
		Category:   category,
		Severity:   severity,
		Code:       code,
		Message:    message,
		Actor:      events.ActorSystem,
		OccurredAt: time.Now().UTC(),
	}
}
