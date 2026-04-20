package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

const (
	backupScheduleInterval = time.Minute
	backupRetryDelay       = 5 * time.Minute
	backupMaxAttempts      = 3
)

// BackupScheduler fires pgBackRest backup tasks on schedule and handles retries.
type BackupScheduler struct {
	nodes     *queries.NodeQuerier
	schedules *queries.BackupScheduleQuerier
	runs      *queries.BackupRunQuerier
	tasks     *queries.TaskResultQuerier
	catalog   *queries.BackupCatalogQuerier
	dispatcher *hub.Dispatcher
	emitter    events.Emitter
}

func NewBackupScheduler(
	nodes *queries.NodeQuerier,
	schedules *queries.BackupScheduleQuerier,
	runs *queries.BackupRunQuerier,
	tasks *queries.TaskResultQuerier,
	catalog *queries.BackupCatalogQuerier,
	dispatcher *hub.Dispatcher,
) *BackupScheduler {
	return &BackupScheduler{
		nodes:      nodes,
		schedules:  schedules,
		runs:       runs,
		tasks:      tasks,
		catalog:    catalog,
		dispatcher: dispatcher,
	}
}

func (s *BackupScheduler) SetEmitter(e events.Emitter) { s.emitter = e }

// Run ticks every minute. On each tick it:
// 1. Retries failed runs whose retry_after has passed.
// 2. Checks each enabled cluster's schedule to see if a new backup tier is due.
func (s *BackupScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(backupScheduleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *BackupScheduler) tick(ctx context.Context) {
	now := time.Now().UTC()

	// Process retries first.
	retries, err := s.runs.PendingRetries(ctx)
	if err != nil {
		slog.Warn("backupscheduler: failed to query pending retries", "error", err)
	} else {
		for _, r := range retries {
			s.dispatchRetry(ctx, r, now)
		}
	}

	// Check schedules for new work.
	enabled, err := s.schedules.ListEnabled(ctx)
	if err != nil {
		slog.Warn("backupscheduler: failed to list enabled schedules", "error", err)
		return
	}

	for _, sched := range enabled {
		s.checkSchedule(ctx, sched, now)
	}
}

func (s *BackupScheduler) checkSchedule(ctx context.Context, sched queries.BackupSchedule, now time.Time) {
	if sched.StanzaName == nil {
		return
	}

	// Determine which backup tier is due at 'now'.
	// Check tiers in priority order: full > diff > incr.
	// We only fire one tier per tick to avoid overlapping dispatches.

	if cronMatches(sched.FullBackupCron, now) {
		s.fireBackup(ctx, sched, "full", now)
		return
	}
	if cronMatches(sched.DiffBackupCron, now) {
		s.fireBackup(ctx, sched, "diff", now)
		return
	}
	// incr: fire if current hour is divisible by the interval.
	if sched.IncrBackupIntervalHrs > 0 && now.Hour()%sched.IncrBackupIntervalHrs == 0 && now.Minute() == 0 {
		s.fireBackup(ctx, sched, "incr", now)
	}
}

func (s *BackupScheduler) fireBackup(ctx context.Context, sched queries.BackupSchedule, backupType string, scheduledAt time.Time) {
	clusterID := sched.ClusterID

	primary := s.findPrimaryNode(ctx, clusterID)
	if primary == nil {
		slog.Warn("backupscheduler: no connected primary node, skipping backup",
			"cluster", clusterID, "type", backupType)
		return
	}

	run, err := s.runs.Create(ctx, clusterID, backupType, scheduledAt, 1)
	if err != nil {
		slog.Warn("backupscheduler: failed to create backup_run", "cluster", clusterID, "error", err)
		return
	}

	taskID, dispErr := s.dispatchBackup(ctx, primary, *sched.StanzaName, backupType)
	if dispErr != nil {
		t := scheduledAt.Add(backupRetryDelay)
		_ = s.runs.SetFailed(ctx, run.ID, dispErr.Error(), &t)
		slog.Warn("backupscheduler: dispatch failed, scheduled retry",
			"cluster", clusterID, "type", backupType, "retry_at", t)
		return
	}

	_ = s.runs.SetDispatched(ctx, run.ID, taskID)
	slog.Info("backupscheduler: backup dispatched",
		"cluster", clusterID, "type", backupType, "task_id", taskID, "node", primary.Hostname)
}

func (s *BackupScheduler) dispatchRetry(ctx context.Context, r queries.BackupRun, now time.Time) {
	if r.Attempt >= backupMaxAttempts {
		errMsg := "abandoned after max retry attempts"
		if r.ErrorMessage != nil {
			errMsg = fmt.Sprintf("abandoned after %d attempts: %s", backupMaxAttempts, *r.ErrorMessage)
		}
		_ = s.runs.SetAbandoned(ctx, r.ID, errMsg)
		slog.Warn("backupscheduler: backup abandoned", "run", r.ID, "cluster", r.ClusterID, "type", r.BackupType)

		if s.emitter != nil {
			s.emitter.Emit(events.New(events.CategoryHA, events.SeverityError, events.CodeFailoverFailed,
				fmt.Sprintf("Backup (%s) abandoned after %d failed attempts", r.BackupType, backupMaxAttempts),
				"scheduler").Cluster(r.ClusterID).Build())
		}
		return
	}

	// Look up stanza name.
	sched, err := s.schedules.Get(ctx, r.ClusterID)
	if err != nil || sched.StanzaName == nil {
		return
	}

	primary := s.findPrimaryNode(ctx, r.ClusterID)
	if primary == nil {
		t := now.Add(backupRetryDelay)
		_ = s.runs.SetFailed(ctx, r.ID, "no primary node available", &t)
		return
	}

	newRun, err := s.runs.Create(ctx, r.ClusterID, r.BackupType, r.ScheduledAt, r.Attempt+1)
	if err != nil {
		return
	}

	taskID, dispErr := s.dispatchBackup(ctx, primary, *sched.StanzaName, r.BackupType)
	if dispErr != nil {
		t := now.Add(backupRetryDelay)
		_ = s.runs.SetFailed(ctx, newRun.ID, dispErr.Error(), &t)
		return
	}
	_ = s.runs.SetDispatched(ctx, newRun.ID, taskID)

	slog.Info("backupscheduler: retry dispatched",
		"cluster", r.ClusterID, "type", r.BackupType, "attempt", r.Attempt+1, "task_id", taskID)
}

func (s *BackupScheduler) dispatchBackup(ctx context.Context, node *queries.Node, stanza, backupType string) (uuid.UUID, error) {
	params, _ := json.Marshal(protocol.PGBackRestBackupParams{
		Stanza: stanza,
		Type:   backupType,
	})
	taskID := uuid.New()
	_ = s.tasks.Create(ctx, node.ID, taskID, string(protocol.TaskPGBackRestBackup), params)
	if err := s.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskPGBackRestBackup,
		Params:      json.RawMessage(params),
		TimeoutSecs: 3600,
	}); err != nil {
		return uuid.Nil, err
	}
	_ = s.tasks.SetSent(ctx, taskID)
	return taskID, nil
}

func (s *BackupScheduler) findPrimaryNode(ctx context.Context, clusterID uuid.UUID) *queries.Node {
	nodes, err := s.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return nil
	}
	var best *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Role != "hyperconverged" && n.Role != "db_only" {
			continue
		}
		if n.AgentStatus != "connected" {
			continue
		}
		if n.PatroniState != nil {
			var ps map[string]any
			if json.Unmarshal(n.PatroniState, &ps) == nil && ps["role"] == "primary" {
				return n
			}
		}
		if best == nil {
			best = n
		}
	}
	return best
}

// ─────────────────────────────────────────────────────────────
// Minimal cron matcher (supports standard 5-field POSIX cron)
//
// Supports: * (wildcard), N (exact), N-M (range), */N (step), N,M,... (list)
// Fields: minute hour day-of-month month day-of-week
// ─────────────────────────────────────────────────────────────

func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	vals := [5]int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	limits := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for i, f := range fields {
		if !cronFieldMatches(f, vals[i], limits[i][0], limits[i][1]) {
			return false
		}
	}
	return true
}

func cronFieldMatches(field string, val, min, max int) bool {
	// Handle comma-separated list first.
	if strings.Contains(field, ",") {
		for _, part := range strings.Split(field, ",") {
			if cronFieldMatches(strings.TrimSpace(part), val, min, max) {
				return true
			}
		}
		return false
	}

	// Step value: */N or start-end/N
	if strings.Contains(field, "/") {
		parts := strings.SplitN(field, "/", 2)
		step, err := strconv.Atoi(parts[1])
		if err != nil || step <= 0 {
			return false
		}
		rangeMin, rangeMax := min, max
		if parts[0] != "*" {
			if strings.Contains(parts[0], "-") {
				r := strings.SplitN(parts[0], "-", 2)
				rangeMin, _ = strconv.Atoi(r[0])
				rangeMax, _ = strconv.Atoi(r[1])
			} else {
				rangeMin, _ = strconv.Atoi(parts[0])
				rangeMax = max
			}
		}
		for v := rangeMin; v <= rangeMax; v += step {
			if v == val {
				return true
			}
		}
		return false
	}

	// Wildcard.
	if field == "*" {
		return true
	}

	// Range: N-M
	if strings.Contains(field, "-") {
		parts := strings.SplitN(field, "-", 2)
		lo, err1 := strconv.Atoi(parts[0])
		hi, err2 := strconv.Atoi(parts[1])
		return err1 == nil && err2 == nil && val >= lo && val <= hi
	}

	// Exact value.
	n, err := strconv.Atoi(field)
	return err == nil && n == val
}
