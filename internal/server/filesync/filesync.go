// Package filesync keeps a folder the same on several nodes, copying through
// the conductor: pgBackRest posix repos, NetBox media and extra sync folders.
//
// Each run lists the folder on every node (path, size, SHA-256, mtime) and
// plans the run: backups mirror the primary exactly; media and extra folders
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
	// sending a large folder); ackTimeoutSecs adds slack for queueing.
	taskTimeoutSecs = 3600
	ackTimeoutSecs  = taskTimeoutSecs + 300

	// relaySendTimeout bounds how long forwarding one chunk may block the
	// source agent's read loop. Must stay well under the 10 s pong deadline
	// (hub/session.go), or the source's connection is dropped while we wait.
	relaySendTimeout = 5 * time.Second

	// connCheckInterval is how often a wait checks the agent is still connected.
	connCheckInterval = 5 * time.Second

	// maxConflictsNamed caps the paths named in a conflict event.
	maxConflictsNamed = 10
)

// ErrBusy is returned by Start when a sync of the same folder is running.
var ErrBusy = errors.New("a sync of this folder is already running")

// Job is one folder sync.
type Job struct {
	ClusterID uuid.UUID
	Name      string // shown in events: "backups", "media" or the folder path
	Root      string // the folder on every node; "" = each node's NetBox media folder
	Nodes     []queries.Node
	// Source set: the other nodes become exact copies of it (backups).
	// uuid.Nil: two-way sync using the record (media, extra folders).
	Source   uuid.UUID
	FileMode os.FileMode // for files written by the sync
	DirMode  os.FileMode // for directories created by the sync
}

// Manager runs syncs, at most one per cluster + folder at a time, and relays
// their listing and chunk messages.
type Manager struct {
	hub        *hub.Hub
	dispatcher *hub.Dispatcher
	tasks      *queries.TaskResultQuerier
	records    *queries.FileSyncQuerier
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

func New(h *hub.Hub, d *hub.Dispatcher, tasks *queries.TaskResultQuerier, records *queries.FileSyncQuerier) *Manager {
	return &Manager{
		hub:        h,
		dispatcher: d,
		tasks:      tasks,
		records:    records,
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

	// 1. List every node at once. An empty folder may be an unmounted share:
	// it is never mirrored, and in a two-way sync it would look like every
	// file was deleted there.
	var wg sync.WaitGroup
	for _, s := range states {
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

	// 2. Plan.
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
		p = mirrorPlan(job.Source, lists)
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
		conflicts = p.conflicts
	}
	for _, s := range states {
		if need := p.bytes[s.node.ID]; s.err == nil && uint64(need) > s.free {
			s.err = fmt.Errorf("needs %d MB free, has %d MB", need>>20, s.free>>20)
		}
	}

	// 3. Copy: receivers accept chunks, then each sending node streams the
	// files it has the right version of, once, in batches that fit in a message.
	receiving := map[uuid.UUID]bool{}
	for _, s := range states {
		if s.err != nil || p.copies[s.node.ID] == 0 {
			continue
		}
		if _, err := m.do(ctx, s.node.ID, protocol.TaskSyncReceive, protocol.SyncReceiveParams{
			SyncID: syncID, Root: job.Root, FileMode: uint32(job.FileMode), DirMode: uint32(job.DirMode),
		}); err != nil {
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
			}); err != nil {
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

	// 4. Delete only on nodes whose copies all arrived.
	for _, s := range states {
		if s.err != nil {
			continue
		}
		for _, batch := range batches(p.deletes[s.node.ID]) {
			if _, err := m.do(ctx, s.node.ID, protocol.TaskSyncDelete, protocol.SyncDeleteParams{
				Root: job.Root, Paths: batch,
			}); err != nil {
				s.err = fmt.Errorf("deleting: %w", err)
				break
			}
			s.deleted += len(batch)
		}
	}

	// 5. Two-way: remember what each node that succeeded now holds. A node
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
	out, err := m.do(ctx, node, protocol.TaskSyncList, protocol.SyncListParams{SyncID: syncID, Root: root})
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
	out, err := m.do(ctx, s.node.ID, protocol.TaskSyncFinish, protocol.SyncFinishParams{SyncID: syncID})
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

// report emits one event per node that receives (every node but a mirror's
// source) and one for any conflicts.
func (m *Manager) report(job Job, states []*nodeState, conflicts []string) {
	from := ""
	for _, s := range states {
		if s.node.ID == job.Source {
			from = " from " + s.node.Hostname
		}
	}
	for _, s := range states {
		if s.node.ID == job.Source {
			continue
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
		named := conflicts[:min(len(conflicts), maxConflictsNamed)]
		msg := fmt.Sprintf("Sync of %s: %d files were changed on more than one node, or changed on one and deleted on another; the newest change was kept: %s",
			job.Name, len(conflicts), strings.Join(named, ", "))
		if more := len(conflicts) - len(named); more > 0 {
			msg += fmt.Sprintf(" and %d more", more)
		}
		m.emit(events.New(events.CategoryCluster, events.SeverityWarn, events.CodeFileSyncConflict, msg, events.ActorSystem).
			Cluster(job.ClusterID).Meta("folder", job.Name).Build())
	}
}

func (m *Manager) emit(e events.Event) {
	if m.emitter != nil {
		m.emitter.Emit(e)
	}
}

// do dispatches one sync task to node and waits for its result. A disconnect
// fails it at once: sync tasks aren't re-sent on reconnect.
func (m *Manager) do(ctx context.Context, node uuid.UUID, t protocol.TaskType, params any) (string, error) {
	raw, _ := json.Marshal(params)
	taskID := uuid.New()
	result, done := m.hub.ExpectTask(taskID)
	defer done()
	sess := m.hub.Get(node)
	if sess == nil {
		return "", errors.New("agent not connected")
	}
	if err := m.dispatcher.DispatchTask(ctx, m.tasks, node, taskID, t, raw, taskTimeoutSecs, ackTimeoutSecs); err != nil {
		return "", err
	}

	deadline := time.NewTimer(ackTimeoutSecs * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(connCheckInterval)
	defer tick.Stop()
	for {
		select {
		case res := <-result:
			if !res.Success {
				return res.Output, errors.New(res.ErrorMsg)
			}
			return res.Output, nil
		case <-tick.C:
			if m.hub.Get(node) != sess {
				return "", errors.New("agent disconnected")
			}
		case <-deadline.C:
			return "", fmt.Errorf("%s timed out", t)
		}
	}
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
