package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/cron"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/averyhabbott/netbox-conductor/internal/server/filesync"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/nodestate"
	"github.com/averyhabbott/netbox-conductor/internal/server/oplock"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

const (
	backupScheduleInterval = time.Minute
	backupRetryDelay       = 5 * time.Minute
	backupMaxAttempts      = 10
	defaultBackupRepoPath  = "/var/lib/pgbackrest"
)

// BackupScheduler fires pgBackRest backup tasks on schedule and handles retries.
// BackupTimeoutSecs is the agent-side limit for one pgbackrest.backup task.
// BackupAckTimeoutSecs is how long the conductor waits for its result: the
// +5 min slack absorbs queueing and result delivery, so the sweeper doesn't
// mark a still-running backup as timed out.
const (
	BackupTimeoutSecs    = 3600
	BackupAckTimeoutSecs = BackupTimeoutSecs + 300
)

type BackupScheduler struct {
	nodes      *queries.NodeQuerier
	schedules  *queries.BackupScheduleQuerier
	runs       *queries.BackupRunQuerier
	tasks      *queries.TaskResultQuerier
	catalog    *queries.BackupCatalogQuerier
	targets    *queries.BackupTargetQuerier
	dispatcher *hub.Dispatcher
	fileSync   *filesync.Manager
	ops        *oplock.Locks
	emitter    events.Emitter
}

func NewBackupScheduler(
	nodes *queries.NodeQuerier,
	schedules *queries.BackupScheduleQuerier,
	runs *queries.BackupRunQuerier,
	tasks *queries.TaskResultQuerier,
	catalog *queries.BackupCatalogQuerier,
	targets *queries.BackupTargetQuerier,
	dispatcher *hub.Dispatcher,
	fileSync *filesync.Manager,
	ops *oplock.Locks,
) *BackupScheduler {
	return &BackupScheduler{
		nodes:      nodes,
		schedules:  schedules,
		runs:       runs,
		tasks:      tasks,
		catalog:    catalog,
		targets:    targets,
		dispatcher: dispatcher,
		fileSync:   fileSync,
		ops:        ops,
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

	// Sync completed task results back to backup_runs before processing retries.
	s.syncCompletedRuns(ctx, now)

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

	if cron.Matches(sched.FullBackupCron, now) {
		s.fireBackup(ctx, sched, "full", now)
		return
	}
	if cron.Matches(sched.DiffBackupCron, now) {
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

	taskID, dispErr := s.dispatchBackup(ctx, primary, *sched.StanzaName, r.BackupType)
	if dispErr != nil {
		t := now.Add(backupRetryDelay)
		_ = s.runs.SetFailed(ctx, r.ID, dispErr.Error(), &t)
		return
	}
	// Update the existing row in-place — no new row per retry.
	_ = s.runs.SetRetrying(ctx, r.ID, taskID, r.Attempt+1)

	slog.Info("backupscheduler: retry dispatched",
		"cluster", r.ClusterID, "type", r.BackupType, "attempt", r.Attempt+1, "task_id", taskID)
}

// dispatchBackup sends a backup to node and holds the cluster's operation lock
// until it finishes. A busy cluster returns an error, which the callers'
// existing retry path turns into a later attempt.
func (s *BackupScheduler) dispatchBackup(ctx context.Context, node *queries.Node, stanza, backupType string) (uuid.UUID, error) {
	unlock, err := s.ops.TryLock(node.ClusterID, "a backup")
	if err != nil {
		return uuid.Nil, err
	}
	params, _ := json.Marshal(protocol.PGBackRestBackupParams{
		Stanza: stanza,
		Type:   backupType,
	})
	taskID := uuid.New()
	if err := s.dispatcher.DispatchTask(ctx, s.tasks, node.ID, taskID, protocol.TaskPGBackRestBackup, params, BackupTimeoutSecs, BackupAckTimeoutSecs); err != nil {
		unlock()
		return uuid.Nil, err
	}
	go func() {
		defer unlock()
		s.tasks.WaitSuccess(context.Background(), taskID, BackupAckTimeoutSecs*time.Second)
	}()
	return taskID, nil
}

func (s *BackupScheduler) findPrimaryNode(ctx context.Context, clusterID uuid.UUID) *queries.Node {
	nodes, err := s.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return nil
	}
	return nodestate.PrimaryDBNode(nodes)
}

func (s *BackupScheduler) syncCompletedRuns(ctx context.Context, now time.Time) {
	completed, err := s.runs.ListRunningWithCompletedTask(ctx)
	if err != nil {
		slog.Warn("backupscheduler: failed to query completed runs", "error", err)
		return
	}
	for _, r := range completed {
		if r.TaskStatus == "success" {
			_ = s.runs.SetSuccess(ctx, r.RunID)
			slog.Info("backupscheduler: backup run succeeded", "run", r.RunID)
			// Auto-refresh catalog and trigger posix sync in the background.
			go s.postBackupSuccess(r.ClusterID)
			continue
		}
		// failure or timeout — extract the most useful error message
		var p struct {
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		errMsg := "task failed"
		if len(r.ResponsePayload) > 0 {
			if json.Unmarshal(r.ResponsePayload, &p) == nil {
				if p.Output != "" {
					errMsg = p.Output
				} else if p.Error != "" {
					errMsg = p.Error
				}
			}
		}
		if r.TaskStatus == "timeout" {
			errMsg = "task timed out: " + errMsg
		}
		retryAfter := now.Add(backupRetryDelay)
		_ = s.runs.SetFailed(ctx, r.RunID, errMsg, &retryAfter)
		slog.Warn("backupscheduler: backup run failed", "run", r.RunID, "task_status", r.TaskStatus, "error", errMsg)
	}
}

// postBackupSuccess runs after a successful backup: refreshes the catalog cache
// and initiates posix repo sync to any configured secondary nodes.
func (s *BackupScheduler) postBackupSuccess(clusterID uuid.UUID) {
	ctx := context.Background()

	sched, err := s.schedules.Get(ctx, clusterID)
	if err != nil || sched.StanzaName == nil {
		return
	}

	primary := s.findPrimaryNode(ctx, clusterID)
	if primary == nil {
		return
	}

	// Dispatch catalog refresh so the UI reflects the new backup.
	catParams, _ := json.Marshal(protocol.PGBackRestCatalogParams{Stanza: *sched.StanzaName})
	catTaskID := uuid.New()
	if err := s.dispatcher.DispatchTask(ctx, s.tasks, primary.ID, catTaskID, protocol.TaskPGBackRestCatalog, catParams, 60, 0); err != nil {
		slog.Warn("backupscheduler: catalog refresh dispatch failed", "node", primary.Hostname, "error", err)
	}

	// For each posix target with sync nodes, sync its repo to them.
	if s.targets == nil || s.fileSync == nil {
		return
	}
	targetList, err := s.targets.ListByCluster(ctx, clusterID)
	if err != nil {
		return
	}
	for _, t := range targetList {
		if t.TargetType != "posix" || len(t.SyncToNodes) == 0 {
			continue
		}
		s.startBackupSync(ctx, primary, t.SyncToNodes, t.PosixPath)
	}
}

// startBackupSync syncs the primary's repo to the target's sync nodes. A sync
// of that repo already running gets this one queued behind it, not dropped.
func (s *BackupScheduler) startBackupSync(ctx context.Context, primary *queries.Node, targetNodeIDs []uuid.UUID, posixPath *string) {
	repoPath := defaultBackupRepoPath
	if posixPath != nil && *posixPath != "" {
		repoPath = *posixPath
	}
	var dests []queries.Node
	for _, id := range targetNodeIDs {
		if id == primary.ID {
			continue
		}
		n, err := s.nodes.GetByID(ctx, id)
		if err != nil {
			slog.Warn("backupscheduler: backup sync node not found", "node", id, "error", err)
			continue
		}
		dests = append(dests, *n)
	}
	if len(dests) == 0 {
		return
	}
	_ = s.fileSync.Start(filesync.BackupJob(primary.ClusterID, repoPath, *primary, dests), true)
	slog.Info("backupscheduler: backup sync started or queued",
		"cluster", primary.ClusterID, "source", primary.Hostname, "targets", len(dests))
}
