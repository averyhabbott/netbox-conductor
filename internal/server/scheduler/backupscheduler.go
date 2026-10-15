package scheduler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
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
	defaultBackupRepoPath  = "/var/lib/pgbackrest"

	// A scheduled round that can't start is tried again after
	// backupScheduleInterval, backupStartTries times in all. In a round, a
	// location whose check fails is checked again after backupScheduleInterval,
	// backupCheckTries times in all.
	backupStartTries = 3
	backupCheckTries = 3
)

// BackupTimeoutSecs is the agent-side limit for one location's backup.
const BackupTimeoutSecs = 3600

// roundTimeoutSecs is the agent-side limit for a round over n locations: each
// may take every check, the waits between them and its backup.
func roundTimeoutSecs(n int) int {
	return n * (backupCheckTries*filesync.CatalogTimeoutSecs +
		(backupCheckTries-1)*int(backupScheduleInterval/time.Second) + BackupTimeoutSecs)
}

// BackupScheduler starts pgBackRest backup rounds on schedule, syncs local
// backups to the other nodes, and tracks which nodes are settled.
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

	// A node is settled once its agent has stayed connected for settleDelay
	// (the failback timer's length); backup syncs leave out nodes that aren't.
	settleDelay func(clusterID uuid.UUID) time.Duration
	settleMu    sync.Mutex
	settling    map[uuid.UUID]*settleTimer // connected nodes
}

type settleTimer struct {
	timer   *time.Timer
	settled bool
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
	settleDelay func(clusterID uuid.UUID) time.Duration,
) *BackupScheduler {
	return &BackupScheduler{
		nodes:       nodes,
		schedules:   schedules,
		runs:        runs,
		tasks:       tasks,
		catalog:     catalog,
		targets:     targets,
		dispatcher:  dispatcher,
		fileSync:    fileSync,
		ops:         ops,
		settleDelay: settleDelay,
		settling:    map[uuid.UUID]*settleTimer{},
	}
}

func (s *BackupScheduler) SetEmitter(e events.Emitter) { s.emitter = e }

// Run ticks every minute. On each tick it records finished rounds' outcomes
// and starts any round due by a cluster's schedule.
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
	s.syncCompletedRuns(ctx)

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

// fireBackup starts a scheduled round, trying backupStartTries times, one
// backupScheduleInterval apart, while it can't start.
func (s *BackupScheduler) fireBackup(ctx context.Context, sched queries.BackupSchedule, backupType string, scheduledAt time.Time) {
	go func() {
		for try := 1; ; try++ {
			_, _, retry, err := s.startRound(ctx, sched.ClusterID, *sched.StanzaName, backupType, scheduledAt, try)
			if err == nil {
				return
			}
			slog.Warn("backupscheduler: backup not started", "cluster", sched.ClusterID, "type", backupType, "try", try, "error", err)
			if !retry {
				return
			}
			if try == backupStartTries {
				s.skipRound(ctx, sched.ClusterID, backupType, scheduledAt, err)
				return
			}
			s.emit(events.New(events.CategoryCluster, events.SeverityWarn, events.CodeBackupPostponed,
				fmt.Sprintf("Scheduled %s backup couldn't start (try %d of %d), trying again in %s: %v",
					backupType, try, backupStartTries, backupScheduleInterval, err),
				events.ActorSystem).Cluster(sched.ClusterID).Build())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backupScheduleInterval):
			}
		}
	}()
}

// skipRound records a scheduled round that never started as abandoned on
// every location, with an error event.
func (s *BackupScheduler) skipRound(ctx context.Context, clusterID uuid.UUID, backupType string, scheduledAt time.Time, err error) {
	msg := fmt.Sprintf("not started in %d tries: %v", backupStartTries, err)
	targets, _ := s.targets.ListByCluster(ctx, clusterID)
	for _, t := range targets {
		if run, cerr := s.runs.Create(ctx, clusterID, backupType, t.RepoIndex, scheduledAt, backupStartTries); cerr == nil {
			_ = s.runs.SetAbandoned(ctx, run.ID, msg)
		}
	}
	s.emit(events.New(events.CategoryCluster, events.SeverityError, events.CodeBackupSkipped,
		fmt.Sprintf("Scheduled %s backup skipped: %s", backupType, msg),
		events.ActorSystem).Cluster(clusterID).Build())
}

// StartBackups starts a round now, once: it returns the primary and the
// round's task, or why it couldn't start.
func (s *BackupScheduler) StartBackups(ctx context.Context, clusterID uuid.UUID, stanza, backupType string, scheduledAt time.Time) (*queries.Node, uuid.UUID, error) {
	primary, taskID, _, err := s.startRound(ctx, clusterID, stanza, backupType, scheduledAt, 1)
	return primary, taskID, err
}

// startRound sends the primary one job that backs up to every storage
// location (see protocol.PGBackRestBackupParams), records a run per location
// and holds the cluster's operation lock until the job is over; then the
// locations backed up are synced. retry: it couldn't start now, but may later.
func (s *BackupScheduler) startRound(ctx context.Context, clusterID uuid.UUID, stanza, backupType string, scheduledAt time.Time, try int) (*queries.Node, uuid.UUID, bool, error) {
	targets, err := s.targets.ListByCluster(ctx, clusterID)
	if err != nil || len(targets) == 0 {
		return nil, uuid.Nil, false, errors.New("no storage locations")
	}
	primary := s.findPrimaryNode(ctx, clusterID)
	if primary == nil {
		return nil, uuid.Nil, true, errors.New("no connected primary")
	}
	unlock, err := s.ops.TryLock(clusterID, "a backup")
	if err != nil {
		return nil, uuid.Nil, true, err
	}
	repos := make([]int, len(targets))
	for i, t := range targets {
		repos[i] = t.RepoIndex
	}
	params, _ := json.Marshal(protocol.PGBackRestBackupParams{
		Stanza: stanza, Type: backupType, Repos: repos,
		CheckTries: backupCheckTries, CheckTimeoutSecs: filesync.CatalogTimeoutSecs,
		CheckRetrySecs: int(backupScheduleInterval / time.Second), BackupTimeoutSecs: BackupTimeoutSecs,
	})
	task, err := s.dispatcher.StartTask(ctx, s.tasks, primary.ID, protocol.TaskPGBackRestBackup, params, roundTimeoutSecs(len(repos)))
	if err != nil {
		unlock()
		return nil, uuid.Nil, true, err
	}
	for _, repo := range repos {
		run, err := s.runs.Create(ctx, clusterID, backupType, repo, scheduledAt, try)
		if err == nil {
			err = s.runs.SetDispatched(ctx, run.ID, task.ID)
		}
		if err != nil {
			slog.Warn("backupscheduler: recording backup run", "cluster", clusterID, "repo", repo, "error", err)
		}
	}
	slog.Info("backupscheduler: backup round dispatched",
		"cluster", clusterID, "type", backupType, "repos", repos, "task_id", task.ID, "node", primary.Hostname)
	go func() {
		out, err := task.Wait(context.Background())
		unlock()
		if err != nil {
			slog.Warn("backupscheduler: backup round", "cluster", clusterID, "task_id", task.ID, "error", err)
		}
		var results []protocol.PGBackRestBackupResult
		_ = json.Unmarshal([]byte(out), &results)
		var done []int
		for _, r := range results {
			if r.Error == "" {
				done = append(done, r.Repo)
			}
		}
		s.afterBackups(clusterID, done)
	}()
	return primary, task.ID, false, nil
}

func (s *BackupScheduler) findPrimaryNode(ctx context.Context, clusterID uuid.UUID) *queries.Node {
	nodes, err := s.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return nil
	}
	return nodestate.PrimaryDBNode(nodes)
}

// syncCompletedRuns records each finished round's outcome on its locations'
// runs, from the task's result (also after a conductor restart).
func (s *BackupScheduler) syncCompletedRuns(ctx context.Context) {
	completed, err := s.runs.ListRunningWithCompletedTask(ctx)
	if err != nil {
		slog.Warn("backupscheduler: failed to query completed runs", "error", err)
		return
	}
	for _, r := range completed {
		var p struct {
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(r.ResponsePayload, &p)
		var results []protocol.PGBackRestBackupResult
		_ = json.Unmarshal([]byte(p.Output), &results)
		errMsg := cmp.Or(p.Error, "no outcome for this location")
		if r.TaskStatus == "timeout" {
			errMsg = "task timed out: " + errMsg
		}
		for _, res := range results {
			if r.RepoIndex != nil && res.Repo == *r.RepoIndex {
				errMsg = res.Error
			}
		}
		if errMsg == "" {
			_ = s.runs.SetSuccess(ctx, r.RunID)
			slog.Info("backupscheduler: backup run succeeded", "run", r.RunID)
			continue
		}
		_ = s.runs.SetFailed(ctx, r.RunID, errMsg)
		slog.Warn("backupscheduler: backup run failed", "run", r.RunID, "task_status", r.TaskStatus, "error", errMsg)
	}
}

// afterBackups runs once a round of backups ends, with the locations (repo
// numbers) that were backed up: it refreshes the catalog cache and syncs each
// of those locations that is synced.
func (s *BackupScheduler) afterBackups(clusterID uuid.UUID, repos []int) {
	if len(repos) == 0 {
		return
	}
	ctx := context.Background()
	sched, err := s.schedules.Get(ctx, clusterID)
	if err != nil || sched.StanzaName == nil {
		return
	}
	if primary := s.findPrimaryNode(ctx, clusterID); primary != nil {
		catParams, _ := json.Marshal(protocol.PGBackRestCatalogParams{Stanza: *sched.StanzaName})
		if err := s.dispatcher.DispatchTask(ctx, s.tasks, primary.ID, uuid.New(), protocol.TaskPGBackRestCatalog, catParams, 60, 0); err != nil {
			slog.Warn("backupscheduler: catalog refresh dispatch failed", "node", primary.Hostname, "error", err)
		}
	}
	s.syncBackups(ctx, clusterID, *sched.StanzaName, func(t queries.BackupTarget) bool { return slices.Contains(repos, t.RepoIndex) })
}

// SyncBackups syncs every synced local-disk location of the cluster, e.g. when
// a node settles or the primary changes. Does nothing until backups are enabled.
func (s *BackupScheduler) SyncBackups(clusterID uuid.UUID) {
	ctx := context.Background()
	sched, err := s.schedules.Get(ctx, clusterID)
	if err != nil || sched.StanzaName == nil || !sched.StanzaInitialized {
		return
	}
	s.syncBackups(ctx, clusterID, *sched.StanzaName, func(queries.BackupTarget) bool { return true })
}

// syncBackups starts the sync of each synced local-disk location that pick
// accepts, from the primary. A sync of that location already running gets this
// one queued behind it. Only the one connected node Patroni reports as primary
// is used: a sync from any other would spread a stale repo. Nodes that aren't
// settled are left out (all of them, when the primary isn't), with an event.
func (s *BackupScheduler) syncBackups(ctx context.Context, clusterID uuid.UUID, stanza string, pick func(queries.BackupTarget) bool) {
	nodes, err := s.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return
	}
	var primary *queries.Node
	for i, n := range nodes {
		if nodestate.IsDBRole(n.Role) && n.AgentStatus == "connected" && nodestate.ExtractPatroniRole(n.PatroniState) == "primary" {
			if primary != nil {
				slog.Warn("backupscheduler: two nodes report primary, backup sync skipped", "cluster", clusterID)
				return
			}
			primary = &nodes[i]
		}
	}
	if primary == nil {
		slog.Info("backupscheduler: no primary, backup sync skipped", "cluster", clusterID)
		return
	}
	targets, err := s.targets.ListByCluster(ctx, clusterID)
	if err != nil {
		return
	}
	for _, t := range targets {
		if t.TargetType != "posix" || len(t.SyncToNodes) == 0 || !pick(t) {
			continue
		}
		if t.PosixPath == nil || *t.PosixPath == "" {
			t.PosixPath = ptr(defaultBackupRepoPath)
		}
		var dests []queries.Node
		for _, n := range nodes {
			if n.ID == primary.ID || !slices.Contains(t.SyncToNodes, n.ID) {
				continue
			}
			switch {
			case !s.settled(primary.ID):
				s.skipNode(t, n, fmt.Sprintf("the primary, %s, hasn't been connected for the failback timer's length yet", primary.Hostname))
			case n.AgentStatus != "connected":
				s.skipNode(t, n, "its agent isn't connected")
			case !s.settled(n.ID):
				s.skipNode(t, n, "its agent hasn't been connected for the failback timer's length yet")
			default:
				dests = append(dests, n)
			}
		}
		if len(dests) == 0 {
			continue
		}
		_ = s.fileSync.Start(filesync.BackupJob(t, stanza, *primary, dests), true)
		slog.Info("backupscheduler: backup sync started or queued",
			"cluster", clusterID, "location", t.Label, "source", primary.Hostname, "targets", len(dests))
	}
}

// skipNode raises the event for a node a backup sync left out.
func (s *BackupScheduler) skipNode(t queries.BackupTarget, n queries.Node, why string) {
	s.emit(events.New(events.CategoryCluster, events.SeverityInfo, events.CodeBackupSyncNodeSkipped,
		fmt.Sprintf("Sync of backups (%s) to %s skipped due to instability: %s; it catches up once settled", t.Label, n.Hostname, why),
		events.ActorSystem).Cluster(n.ClusterID).Node(n.ID).Meta("folder", "backups").Build())
}

// OnNodeConnect starts nodeID's settle timer; when it fires, the node is
// settled and the cluster's backups are synced, to catch it up.
func (s *BackupScheduler) OnNodeConnect(nodeID, clusterID uuid.UUID) {
	st := &settleTimer{}
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	if old := s.settling[nodeID]; old != nil {
		old.timer.Stop()
	}
	s.settling[nodeID] = st
	st.timer = time.AfterFunc(s.settleDelay(clusterID), func() {
		s.settleMu.Lock()
		current := s.settling[nodeID] == st
		st.settled = current
		s.settleMu.Unlock()
		if current {
			slog.Info("backupscheduler: node settled", "node", nodeID)
			s.SyncBackups(clusterID)
		}
	})
}

// OnNodeDisconnect unsettles nodeID.
func (s *BackupScheduler) OnNodeDisconnect(nodeID uuid.UUID) {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	if st := s.settling[nodeID]; st != nil {
		st.timer.Stop()
		delete(s.settling, nodeID)
	}
}

func (s *BackupScheduler) settled(nodeID uuid.UUID) bool {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	st := s.settling[nodeID]
	return st != nil && st.settled
}

func (s *BackupScheduler) emit(e events.Event) {
	if s.emitter != nil {
		s.emitter.Emit(e)
	}
}

func ptr[T any](v T) *T { return &v }
