package filesync

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

func TestMirrorPlan(t *testing.T) {
	src, dst := uuid.New(), uuid.New()
	p := mirrorPlan(src, map[uuid.UUID][]protocol.SyncFile{
		src: {
			{Path: "same", Size: 1, SHA256: "a"},
			{Path: "changed", Size: 2, SHA256: "b"},
			{Path: "new", Size: 3, SHA256: "c"},
			{Path: "stale" + protocol.SyncPartialSuffix, Size: 9, SHA256: "z"},
		},
		dst: {
			{Path: "same", Size: 1, SHA256: "a"},
			{Path: "changed", Size: 2, SHA256: "old"},
			{Path: "gone", Size: 4, SHA256: "d"},
			{Path: "x" + protocol.SyncPartialSuffix, Size: 5, SHA256: "e"},
		},
	})
	if got := slices.Sorted(maps.Keys(p.sends[src])); !slices.Equal(got, []string{"changed", "new"}) || p.bytes[dst] != 5 || p.copies[dst] != 2 {
		t.Errorf("copies = %v (%d bytes), want [changed new] (5 bytes)", got, p.bytes[dst])
	}
	if !slices.Equal(p.deletes[dst], []string{"gone", "x" + protocol.SyncPartialSuffix}) {
		t.Errorf("dst deletes = %v", p.deletes[dst])
	}
	if !slices.Equal(p.deletes[src], []string{"stale" + protocol.SyncPartialSuffix}) {
		t.Errorf("src deletes = %v, want its partial file", p.deletes[src])
	}
}

// twoWayCase is three nodes' listings: a and b synced at t1, c at t0.
func twoWayCase(a, b, c []protocol.SyncFile, rec map[string][3]string, deleted ...string) (plan, [3]uuid.UUID) {
	ids := [3]uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	t0 := time.Now()
	r := queries.FileSyncRecord{
		Files:    map[uuid.UUID]map[string]string{ids[0]: {}, ids[1]: {}, ids[2]: {}},
		SyncedAt: map[uuid.UUID]time.Time{ids[0]: t0.Add(time.Hour), ids[1]: t0.Add(time.Hour), ids[2]: t0},
		Deleted:  map[string]bool{},
	}
	for path, sums := range rec {
		for i, sum := range sums {
			if sum != "" {
				r.Files[ids[i]][path] = sum
			}
		}
	}
	for _, d := range deleted {
		r.Deleted[d] = true
	}
	lists := map[uuid.UUID][]protocol.SyncFile{ids[0]: a, ids[1]: b}
	if c != nil {
		lists[ids[2]] = c
	}
	return twoWayPlan(lists, r), ids
}

func f(path, sum string, mtime int64) protocol.SyncFile {
	return protocol.SyncFile{Path: path, Size: 1, SHA256: sum, MTime: mtime}
}

func TestTwoWayPlan(t *testing.T) {
	t.Run("new file on one node is copied to the others", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "1", 1), f("new", "n", 1)}, []protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1)},
			map[string][3]string{"x": {"1", "1", "1"}})
		if got := p.sends[n[0]]["new"]; len(got) != 2 || p.files["new"] != "n" || len(p.conflicts) != 0 {
			t.Errorf("new → %v, files %v, conflicts %v", got, p.files, p.conflicts)
		}
	})
	t.Run("delete on one node is deleted everywhere and remembered", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1), f("gone", "g", 1)}, []protocol.SyncFile{f("x", "1", 1), f("gone", "g", 1)},
			map[string][3]string{"x": {"1", "1", "1"}, "gone": {"g", "g", "g"}})
		if !slices.Equal(p.deleted, []string{"gone"}) || len(p.deletes[n[1]]) != 1 || len(p.deletes[n[2]]) != 1 || len(p.sends) != 0 {
			t.Errorf("deleted %v, deletes %v, sends %v", p.deleted, p.deletes, p.sends)
		}
		if _, ok := p.files["gone"]; ok {
			t.Error("gone still in the new record")
		}
	})
	t.Run("delete marker reaches a node that was offline", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1), f("gone", "g", 1)},
			map[string][3]string{"x": {"1", "1", "1"}, "gone": {"", "", "g"}}, "gone")
		if !slices.Equal(p.deletes[n[2]], []string{"gone"}) || len(p.sends) != 0 {
			t.Errorf("deletes %v, sends %v", p.deletes, p.sends)
		}
	})
	t.Run("edit is copied everywhere", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "2", 5)}, []protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1)},
			map[string][3]string{"x": {"1", "1", "1"}})
		if len(p.sends[n[0]]["x"]) != 2 || p.files["x"] != "2" || len(p.conflicts) != 0 {
			t.Errorf("sends %v, files %v", p.sends, p.files)
		}
	})
	t.Run("edits on two nodes: newest wins, conflict noted", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "2", 5)}, []protocol.SyncFile{f("x", "3", 9)}, []protocol.SyncFile{f("x", "1", 1)},
			map[string][3]string{"x": {"1", "1", "1"}})
		if len(p.sends[n[1]]["x"]) != 2 || p.files["x"] != "3" || !slices.Equal(p.conflicts, []string{"x"}) {
			t.Errorf("sends %v, files %v, conflicts %v", p.sends, p.files, p.conflicts)
		}
	})
	t.Run("edit beats a delete", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "2", 5)}, []protocol.SyncFile{}, []protocol.SyncFile{f("x", "1", 1)},
			map[string][3]string{"x": {"1", "1", "1"}})
		if len(p.sends[n[0]]["x"]) != 2 || len(p.deleted) != 0 || !slices.Equal(p.conflicts, []string{"x"}) {
			t.Errorf("sends %v, deleted %v, conflicts %v", p.sends, p.deleted, p.conflicts)
		}
	})
	t.Run("node that missed an edit catches up from the latest synced", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "2", 1)}, []protocol.SyncFile{f("x", "2", 1)}, []protocol.SyncFile{f("x", "1", 9)},
			map[string][3]string{"x": {"2", "2", "1"}})
		// a and b synced together with the same copy, so either may send it.
		sent := append(p.sends[n[0]]["x"], p.sends[n[1]]["x"]...)
		if !slices.Equal(sent, []uuid.UUID{n[2]}) || p.files["x"] != "2" {
			t.Errorf("sends %v, files %v", p.sends, p.files)
		}
	})
	t.Run("copy that arrived in a failed sync is not an edit", func(t *testing.T) {
		// c received gone=g but failed, so its record lacks it; a deleted it since.
		p, n := twoWayCase([]protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("x", "1", 1), f("gone", "g", 9)},
			map[string][3]string{"x": {"1", "1", "1"}, "gone": {"g", "", ""}})
		if !slices.Equal(p.deletes[n[2]], []string{"gone"}) || len(p.conflicts) != 0 {
			t.Errorf("deletes %v, conflicts %v", p.deletes, p.conflicts)
		}
	})
	t.Run("first sync copies only", func(t *testing.T) {
		p, n := twoWayCase([]protocol.SyncFile{f("x", "1", 1)}, []protocol.SyncFile{f("y", "2", 1), f("x", "9", 9)}, nil, nil)
		p.files = nil // compared below via sends
		if len(p.deleted) != 0 || len(p.deletes) != 0 {
			t.Errorf("first sync deletes: %v %v", p.deleted, p.deletes)
		}
		if !slices.Equal(p.sends[n[1]]["x"], []uuid.UUID{n[0]}) || !slices.Equal(p.sends[n[1]]["y"], []uuid.UUID{n[0]}) {
			t.Errorf("sends %v, want x (newest) and y from b to a", p.sends)
		}
	})
	t.Run("new node gets the agreed version, not its own", func(t *testing.T) {
		ids := [2]uuid.UUID{uuid.New(), uuid.New()}
		p := twoWayPlan(map[uuid.UUID][]protocol.SyncFile{ids[0]: {f("x", "1", 1)}, ids[1]: {f("x", "old", 9)}},
			queries.FileSyncRecord{Files: map[uuid.UUID]map[string]string{ids[0]: {"x": "1"}}, SyncedAt: map[uuid.UUID]time.Time{ids[0]: time.Now()}})
		if !slices.Equal(p.sends[ids[0]]["x"], []uuid.UUID{ids[1]}) {
			t.Errorf("sends %v", p.sends)
		}
	})
}

func TestSendOrderIndexLast(t *testing.T) {
	routes := map[string][]uuid.UUID{
		"backup/s/backup.info": nil, "archive/s/archive.info.copy": nil,
		"backup/s/20261012F/backup.manifest": nil, "archive/s/16-1/0001": nil,
	}
	got := sendOrder(routes)
	want := []string{"archive/s/16-1/0001", "backup/s/20261012F/backup.manifest", "archive/s/archive.info.copy", "backup/s/backup.info"}
	if !slices.Equal(got, want) {
		t.Errorf("sendOrder = %v, want %v", got, want)
	}
}

func TestBatches(t *testing.T) {
	if b := batches(nil); len(b) != 0 {
		t.Errorf("batches(nil) = %v", b)
	}
	long := strings.Repeat("a", batchBytes/2)
	b := batches([]string{long, long, "x"})
	if len(b) != 2 || len(b[0]) != 1 || len(b[1]) != 2 {
		t.Errorf("batch sizes = %d, want [1 2]", len(b))
	}
}
