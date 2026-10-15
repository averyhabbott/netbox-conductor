// Package filesync keeps a folder the same on several nodes, copying through
// the conductor: pgBackRest posix repos, NetBox media and extra sync folders.
//
// Each run lists the folder on every node (path, size, SHA-256, mtime) and
// plans the run: backups mirror the main server, after gathering to it the
// WAL other nodes archived while they were primary; media and extra folders
// sync two ways, comparing each node with the record of its last successful
// sync to tell new, changed and deleted files apart. Each needed file is
// streamed once from a node that has the right version, and each chunk is
// forwarded to just the nodes that need it. Nothing is stored on the
// conductor but the record. Deletes happen only on a node that received every
// copy it needed.
package filesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

const (
	// taskTimeoutSecs is the agent-side limit for one sync task (listing or
	// sending a large folder).
	taskTimeoutSecs = 3600

	// relaySendTimeout bounds how long forwarding one chunk may block the
	// source agent's read loop. Must stay well under the 10 s pong deadline
	// (hub/session.go), or the source's connection is dropped while we wait.
	relaySendTimeout = 5 * time.Second

	// maxConflictsNamed caps the paths named in a conflict event.
	maxConflictsNamed = 10

	// PathTestTimeoutSecs is the agent-side limit for one storage path test;
	// CatalogTimeoutSecs for `pgbackrest info`, as for every catalog refresh.
	PathTestTimeoutSecs = 15
	CatalogTimeoutSecs  = 60
)

// ErrBusy is returned by Start when a sync of the same folder is running.
var ErrBusy = errors.New("a sync of this folder is already running")

// Job is one folder sync.
type Job struct {
	ClusterID uuid.UUID
	Name      string // shown in events: "backups", "media" or the folder path
	Root      string // the folder on every node; "" = each node's NetBox media folder
	Nodes     []queries.Node
	// Source set: a backup sync (see backupPlan), Source is the main server.
	// uuid.Nil: two-way sync using the record (media, extra folders).
	Source   uuid.UUID
	Backup   *Backup
	FileMode os.FileMode // for files written by a two-way sync
	DirMode  os.FileMode // for directories created by a two-way sync
}

// Backup is the storage location a backup sync is for.
type Backup struct {
	Target queries.BackupTarget
	Stanza string
}

// Manager runs syncs, at most one per cluster + folder at a time, and relays
// their listing and chunk messages.
type Manager struct {
	hub        *hub.Hub
	dispatcher *hub.Dispatcher
	tasks      *queries.TaskResultQuerier
	records    *queries.FileSyncQuerier
	targets    *queries.BackupTargetQuerier
	emitter    events.Emitter

	mu     sync.Mutex
	active map[string]*Job // cluster|root → job queued behind the running one (nil: none)
	runs   map[string]*run // sync ID → in-flight run
}

// run is the relay state of one in-flight sync.
type run struct {
	mu      sync.Mutex
	lists   map[uuid.UUID][]protocol.SyncFile // every participant has a key
	source  uuid.UUID                         // node sending the current batch
	routes  map[string][]uuid.UUID            // path → receiving nodes, for the current batch
	dropped map[uuid.UUID]bool                // receivers cut off for falling behind
}

func New(h *hub.Hub, d *hub.Dispatcher, tasks *queries.TaskResultQuerier, records *queries.FileSyncQuerier, targets *queries.BackupTargetQuerier) *Manager {
	return &Manager{
		hub:        h,
		dispatcher: d,
		tasks:      tasks,
		records:    records,
		targets:    targets,
		active:     make(map[string]*Job),
		runs:       make(map[string]*run),
	}
}

func (m *Manager) SetEmitter(e events.Emitter) { m.emitter = e }

// Start runs job in the background. If a sync of the same folder is already
// running, queue=true has job start when it ends (replacing any job already
// queued); queue=false returns ErrBusy.
func (m *Manager) Start(job Job, queue bool) error {
	key := job.ClusterID.String() + "|" + job.Root
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.active[key]; busy {
		if !queue {
			return ErrBusy
		}
		m.active[key] = &job
		return nil
	}
	m.active[key] = nil
	go m.loop(key, job)
	return nil
}

func (m *Manager) loop(key string, job Job) {
	for {
		m.sync(job)
		m.mu.Lock()
		next := m.active[key]
		if next == nil {
			delete(m.active, key)
			m.mu.Unlock()
			return
		}
		m.active[key] = nil
		m.mu.Unlock()
		job = *next
	}
}

// nodeState tracks one node through a run; err set means it failed.
type nodeState struct {
	node    queries.Node
	free    uint64
	err     error
	copied  int
	deleted int
}

func (m *Manager) sync(job Job) {
	ctx := context.Background()
	syncID := uuid.NewString()
	log := slog.With("sync_id", syncID, "cluster", job.ClusterID, "folder", job.Name)
	log.Info("filesync: starting", "nodes", len(job.Nodes), "two_way", job.Source == uuid.Nil)

	r := &run{lists: map[uuid.UUID][]protocol.SyncFile{}, dropped: map[uuid.UUID]bool{}}
	states := make([]*nodeState, len(job.Nodes))
	byID := make(map[uuid.UUID]*nodeState, len(job.Nodes))
	for i, n := range job.Nodes {
		r.lists[n.ID] = nil
		states[i] = &nodeState{node: n}
		byID[n.ID] = states[i]
	}
	m.mu.Lock()
	m.runs[syncID] = r
	m.mu.Unlock()
	var conflicts []string
	defer func() {
		m.mu.Lock()
		delete(m.runs, syncID)
		m.mu.Unlock()
		m.report(job, states, conflicts)
	}()

	// 1. A backup sync first tests the storage path on every node, then reads
	// the main server's retention. A failing node is left out.
	var keep func(string) bool
	if job.Backup != nil {
		nodes := make([]queries.Node, len(states))
		for i, s := range states {
			nodes[i] = s.node
		}
		for id, err := range m.TestPath(ctx, job.Backup.Target, nodes) {
			if err != nil {
				byID[id].err = fmt.Errorf("path test: %w", err)
			}
		}
		if src := byID[job.Source]; src.err == nil {
			out, err := m.do(ctx, job.Source, protocol.TaskPGBackRestCatalog, protocol.PGBackRestCatalogParams{Stanza: job.Backup.Stanza}, CatalogTimeoutSecs)
			if err == nil {
				keep, err = walKeep([]byte(out), job.Backup.Stanza, job.Backup.Target.RepoIndex)
			}
			src.err = err
		}
	}

	// 2. List every node at once. An empty folder may be an unmounted share:
	// the main server's is never mirrored, and in a two-way sync it would look
	// like every file was deleted there.
	var wg sync.WaitGroup
	for _, s := range states {
		if s.err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := m.list(ctx, r, syncID, job.Root, s.node.ID)
			if err == nil && res.Files == 0 && (job.Source == uuid.Nil || s.node.ID == job.Source) {
				err = errors.New("folder is empty; left out of this sync (is it mounted?)")
			}
			s.free, s.err = res.FreeBytes, err
		}()
	}
	wg.Wait()
	lists := map[uuid.UUID][]protocol.SyncFile{}
	for _, s := range states {
		if s.err == nil {
			lists[s.node.ID] = r.lists[s.node.ID]
		}
	}

	// 3. Plan.
	var p plan
	if job.Source != uuid.Nil {
		if src := byID[job.Source]; src.err != nil {
			for _, s := range states {
				if s != src {
					s.err = fmt.Errorf("source %s: %w", src.node.Hostname, src.err)
				}
			}
			return
		}
		p = backupPlan(job.Source, lists, keep)
	} else {
		if len(lists) == 0 {
			return
		}
		rec, err := m.records.Load(ctx, job.ClusterID, job.Root)
		if err != nil {
			for _, s := range states {
				if s.err == nil {
					s.err = fmt.Errorf("loading the sync record: %w", err)
				}
			}
			return
		}
		p = twoWayPlan(lists, rec)
	}
	conflicts = p.conflicts
	for _, s := range states {
		if need := p.bytes[s.node.ID]; s.err == nil && uint64(need) > s.free {
			s.err = fmt.Errorf("needs %d MB free, has %d MB", need>>20, s.free>>20)
		}
	}

	// 4. Copy: receivers accept chunks, then each sending node streams the
	// files it has the right version of, once, in batches that fit in a message.
	receiving := map[uuid.UUID]bool{}
	for _, s := range states {
		if s.err != nil || p.copies[s.node.ID] == 0 {
			continue
		}
		if _, err := m.do(ctx, s.node.ID, protocol.TaskSyncReceive, protocol.SyncReceiveParams{
			SyncID: syncID, Root: job.Root, FileMode: uint32(job.FileMode), DirMode: uint32(job.DirMode),
			AsPostgres: job.Backup != nil,
		}, taskTimeoutSecs); err != nil {
			s.err = err
			continue
		}
		receiving[s.node.ID] = true
	}
	sendErr := map[uuid.UUID]error{} // receiver → why a sender it needed failed
	for _, from := range slices.SortedFunc(maps.Keys(p.sends), func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }) {
		routes := map[string][]uuid.UUID{}
		for path, to := range p.sends[from] {
			if to = slices.DeleteFunc(slices.Clone(to), func(n uuid.UUID) bool { return !receiving[n] }); len(to) > 0 {
				routes[path] = to
			}
		}
		for _, batch := range batches(sendOrder(routes)) {
			r.mu.Lock()
			r.source = from
			r.routes = make(map[string][]uuid.UUID, len(batch))
			for _, path := range batch {
				r.routes[path] = routes[path]
			}
			r.mu.Unlock()
			if _, err := m.do(ctx, from, protocol.TaskSyncSend, protocol.SyncSendParams{
				SyncID: syncID, Root: job.Root, Paths: batch,
			}, taskTimeoutSecs); err != nil {
				for _, to := range routes {
					for _, n := range to {
						sendErr[n] = fmt.Errorf("%s failed while sending: %w", byID[from].node.Hostname, err)
					}
				}
				break
			}
		}
	}
	for _, s := range states {
		if receiving[s.node.ID] {
			s.err = m.finish(ctx, r, syncID, s, p.copies[s.node.ID], sendErr[s.node.ID])
		}
	}

	// 5. Delete only on nodes whose copies all arrived.
	for _, s := range states {
		if s.err != nil {
			continue
		}
		for _, batch := range batches(p.deletes[s.node.ID]) {
			if _, err := m.do(ctx, s.node.ID, protocol.TaskSyncDelete, protocol.SyncDeleteParams{
				Root: job.Root, Paths: batch, AsPostgres: job.Backup != nil,
			}, taskTimeoutSecs); err != nil {
				s.err = fmt.Errorf("deleting: %w", err)
				break
			}
			s.deleted += len(batch)
		}
	}

	// 6. Two-way: remember what each node that succeeded now holds. A node
	// that failed keeps its last good record.
	if job.Source == uuid.Nil {
		var synced []uuid.UUID
		for _, s := range states {
			if s.err == nil {
				synced = append(synced, s.node.ID)
			}
		}
		if err := m.records.Save(ctx, job.ClusterID, job.Root, synced, p.files, p.deleted); err != nil {
			for _, s := range states {
				if s.err == nil {
					s.err = fmt.Errorf("saving the sync record: %w", err)
				}
			}
		}
	}
}

// list asks node for its listing and checks every piece of it arrived.
func (m *Manager) list(ctx context.Context, r *run, syncID, root string, node uuid.UUID) (protocol.SyncListResult, error) {
	var res protocol.SyncListResult
	out, err := m.do(ctx, node, protocol.TaskSyncList, protocol.SyncListParams{SyncID: syncID, Root: root}, taskTimeoutSecs)
	if err == nil {
		err = json.Unmarshal([]byte(out), &res)
	}
	if err != nil {
		return res, fmt.Errorf("listing: %w", err)
	}
	r.mu.Lock()
	got := len(r.lists[node])
	r.mu.Unlock()
	if got != res.Files {
		return res, fmt.Errorf("listing incomplete: received %d of %d entries", got, res.Files)
	}
	return res, nil
}

// finish closes s's receive side and checks every file it needed arrived.
func (m *Manager) finish(ctx context.Context, r *run, syncID string, s *nodeState, want int, sendErr error) error {
	out, err := m.do(ctx, s.node.ID, protocol.TaskSyncFinish, protocol.SyncFinishParams{SyncID: syncID}, taskTimeoutSecs)
	var res protocol.SyncFinishResult
	if err == nil {
		err = json.Unmarshal([]byte(out), &res)
	}
	s.copied = res.Copied
	r.mu.Lock()
	dropped := r.dropped[s.node.ID]
	r.mu.Unlock()
	switch {
	case sendErr != nil:
		return sendErr
	case dropped:
		return errors.New("cut off for not keeping up; the next sync continues")
	case err != nil:
		return fmt.Errorf("finishing: %w", err)
	case res.Errors > 0:
		return fmt.Errorf("%d files failed: %s", res.Errors, strings.Join(res.Failed, "; "))
	case res.Copied+res.Skipped != want:
		return fmt.Errorf("received %d of %d files", res.Copied+res.Skipped, want)
	}
	return nil
}

// report emits one event per node that receives (a backup sync's main server
// only when WAL was gathered to it or it failed) and one for any conflicts.
func (m *Manager) report(job Job, states []*nodeState, conflicts []string) {
	from := ""
	for _, s := range states {
		if s.node.ID == job.Source {
			from = " from " + s.node.Hostname
		}
	}
	for _, s := range states {
		from := from
		if s.node.ID == job.Source {
			if s.err == nil && s.copied == 0 {
				continue
			}
			from = " (WAL gathered to the main server)"
		}
		sev, code := events.SeverityInfo, events.CodeFileSyncCompleted
		msg := fmt.Sprintf("Sync of %s%s to %s: copied %d, deleted %d",
			job.Name, from, s.node.Hostname, s.copied, s.deleted)
		if s.err != nil {
			sev, code = events.SeverityError, events.CodeFileSyncFailed
			msg = fmt.Sprintf("Sync of %s%s to %s failed (copied %d, deleted %d): %v",
				job.Name, from, s.node.Hostname, s.copied, s.deleted, s.err)
			slog.Warn("filesync: node failed", "cluster", job.ClusterID, "folder", job.Name, "node", s.node.Hostname, "error", s.err)
		} else {
			slog.Info("filesync: node done", "cluster", job.ClusterID, "folder", job.Name, "node", s.node.Hostname, "copied", s.copied, "deleted", s.deleted)
		}
		m.emit(events.New(events.CategoryCluster, sev, code, msg, events.ActorSystem).
			Cluster(job.ClusterID).Node(s.node.ID).Meta("folder", job.Name).Build())
	}
	if len(conflicts) > 0 {
		slog.Warn("filesync: conflicts", "cluster", job.ClusterID, "folder", job.Name, "paths", conflicts)
		named := strings.Join(conflicts[:min(len(conflicts), maxConflictsNamed)], ", ")
		if more := len(conflicts) - maxConflictsNamed; more > 0 {
			named += fmt.Sprintf(" and %d more", more)
		}
		sev, code := events.SeverityWarn, events.CodeFileSyncConflict
		msg := fmt.Sprintf("Sync of %s: %d files were changed on more than one node, or changed on one and deleted on another; the newest change was kept: %s",
			job.Name, len(conflicts), named)
		if job.Backup != nil {
			sev, code = events.SeverityError, events.CodeBackupWALConflict
			msg = fmt.Sprintf("Sync of %s: %d WAL files exist with different contents on different nodes (two nodes were primary at once); they were left as they are on every node: %s",
				job.Name, len(conflicts), named)
		}
		m.emit(events.New(events.CategoryCluster, sev, code, msg, events.ActorSystem).
			Cluster(job.ClusterID).Meta("folder", job.Name).Build())
	}
}

// TestPath runs the storage path test of a local-disk location on each node at
// once, records the results on the location and raises an event per failing
// node. A node that can't be reached, or whose test didn't finish (no result),
// gets an error but no record or event: the path wasn't shown to fail.
func (m *Manager) TestPath(ctx context.Context, target queries.BackupTarget, nodes []queries.Node) map[uuid.UUID]error {
	var mu sync.Mutex
	errs := make(map[uuid.UUID]error, len(nodes))
	results := map[string]string{}
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.hub.Get(n.ID) == nil {
				mu.Lock()
				errs[n.ID] = errors.New("agent not connected")
				mu.Unlock()
				return
			}
			_, err := m.do(ctx, n.ID, protocol.TaskPGBackRestTestPath, protocol.PGBackRestTestPathParams{Path: strVal(target.PosixPath)}, PathTestTimeoutSecs)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, hub.ErrNoResult) {
				errs[n.ID] = fmt.Errorf("test didn't finish: %w", err)
				return
			}
			errs[n.ID] = err
			results[n.ID.String()] = ""
			if err != nil {
				results[n.ID.String()] = err.Error()
				m.emit(events.New(events.CategoryCluster, events.SeverityError, events.CodeBackupPathFailed,
					fmt.Sprintf("Backup storage %q (%s) doesn't work on %s: %v", target.Label, strVal(target.PosixPath), n.Hostname, err),
					events.ActorSystem).Cluster(target.ClusterID).Node(n.ID).Build())
			}
		}()
	}
	wg.Wait()
	if err := m.targets.SetPathCheck(ctx, target.ID, results); err != nil {
		slog.Warn("filesync: saving path test results", "target", target.ID, "error", err)
	}
	return errs
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (m *Manager) emit(e events.Event) {
	if m.emitter != nil {
		m.emitter.Emit(e)
	}
}

// do runs one sync task on node, with the agent-side limit timeoutSecs, and
// waits for its outcome (hub.Task.Wait).
func (m *Manager) do(ctx context.Context, node uuid.UUID, t protocol.TaskType, params any, timeoutSecs int) (string, error) {
	raw, _ := json.Marshal(params)
	return m.dispatcher.RunTask(ctx, m.tasks, node, t, raw, timeoutSecs)
}

func (m *Manager) getRun(syncID string) *run {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[syncID]
}

// HandleListing stores one piece of a node's sync.list result.
func (m *Manager) HandleListing(node uuid.UUID, payload json.RawMessage) {
	var l protocol.SyncListingPayload
	if err := json.Unmarshal(payload, &l); err != nil {
		return
	}
	r := m.getRun(l.SyncID)
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.lists[node]; ok {
		r.lists[node] = append(r.lists[node], l.Files...)
	}
}

// HandleChunk forwards a chunk from the node sending the current batch to
// each node that needs its file. A receiver that can't take it within relaySendTimeout is
// cut off for the rest of the run; it catches up on the next sync.
func (m *Manager) HandleChunk(node uuid.UUID, env protocol.Envelope) {
	var c struct {
		SyncID string `json:"sync_id"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(env.Payload, &c); err != nil {
		return
	}
	r := m.getRun(c.SyncID)
	if r == nil {
		return
	}
	r.mu.Lock()
	if node != r.source {
		r.mu.Unlock()
		return
	}
	var to []uuid.UUID
	for _, d := range r.routes[c.Path] {
		if !r.dropped[d] {
			to = append(to, d)
		}
	}
	r.mu.Unlock()
	for _, d := range to {
		if s := m.hub.Get(d); s == nil || !s.SendWait(env, relaySendTimeout) {
			r.mu.Lock()
			r.dropped[d] = true
			r.mu.Unlock()
			slog.Error("filesync: destination cut off: agent offline or not keeping up", "sync_id", c.SyncID, "node", d)
		}
	}
}
