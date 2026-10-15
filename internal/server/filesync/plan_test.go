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

func TestBackupPlanMirrors(t *testing.T) {
	src, dst := uuid.New(), uuid.New()
	p := backupPlan(src, map[uuid.UUID][]protocol.SyncFile{
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
	}, func(string) bool { return true })
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

const arch = "archive/db/17-1/"

func TestBackupPlanGathers(t *testing.T) {
	main, old, third := uuid.New(), uuid.New(), uuid.New()
	keep, err := walKeep([]byte(`[{"name":"db","archive":[{"database":{"id":1,"repo-key":1},"id":"17-1","min":"000000080000000000000005"}],"repo":[{"key":1,"status":{"code":0}}]}]`), "db", 1)
	if err != nil {
		t.Fatal(err)
	}
	seg := func(name, sum string) protocol.SyncFile {
		return protocol.SyncFile{Path: arch + name[:16] + "/" + name + "-" + sum + ".gz", Size: 1, SHA256: sum}
	}
	hist := func(tl, sum string) protocol.SyncFile {
		return protocol.SyncFile{Path: arch + tl + ".history", Size: 1, SHA256: sum}
	}
	mainSeg := seg("000000090000000000000006", "m6")
	newer := seg("000000090000000000000007", "o7")           // archived on old while primary: gathered
	older := seg("000000080000000000000004", "o4")           // before the cutoff: expired, deleted
	partial := seg("000000080000000000000006.partial", "o6") // after it: gathered
	splitA, splitB := seg("0000000A0000000000000008", "a8"), seg("0000000A0000000000000008", "b8")
	backup := protocol.SyncFile{Path: "backup/db/20261013-192401F/backup.manifest", Size: 1, SHA256: "bk"}
	p := backupPlan(main, map[uuid.UUID][]protocol.SyncFile{
		main:  {mainSeg, hist("00000009", "h9"), splitA},
		old:   {mainSeg, newer, older, partial, hist("0000000A", "hA"), hist("00000009", "h9-other"), splitB, backup},
		third: {},
	}, keep)

	if got := slices.Sorted(maps.Keys(p.sends[old])); !slices.Equal(got, []string{partial.Path, newer.Path, arch + "0000000A.history"}) {
		t.Errorf("gathered from old = %v", got)
	}
	if to := p.sends[old][newer.Path]; !slices.Contains(to, main) || !slices.Contains(to, third) {
		t.Errorf("newer WAL goes to %v, want main and third", to)
	}
	if !slices.Equal(p.deletes[old], []string{older.Path, backup.Path}) {
		t.Errorf("old deletes = %v, want only the expired WAL and its own backup", p.deletes[old])
	}
	if len(p.deletes[main]) != 0 {
		t.Errorf("main deletes = %v, want none", p.deletes[main])
	}
	// The split segment and the .history with two contents stay as they are on
	// main and old; third, holding neither, gets main's.
	if !slices.Equal(p.conflicts, []string{arch + "00000009.history", arch + "0000000A00000000/0000000A0000000000000008"}) {
		t.Errorf("conflicts = %v", p.conflicts)
	}
	for _, to := range p.sends[main] {
		if slices.Contains(to, old) {
			t.Errorf("main sends to old: %v", p.sends[main])
		}
	}
	if !slices.Contains(p.sends[main][splitA.Path], third) || !slices.Contains(p.sends[main][arch+"00000009.history"], third) {
		t.Errorf("third doesn't get main's conflicting files: %v", p.sends[main])
	}
}

func TestWalKeep(t *testing.T) {
	if _, err := walKeep([]byte(`[{"name":"db","repo":[{"key":2,"status":{"code":99,"message":"missing stanza path"}}]}]`), "db", 2); err == nil {
		t.Error("broken repo: want an error")
	}
	if _, err := walKeep([]byte(`[{"name":"db","repo":[{"key":1,"status":{"code":2,"message":"no valid backups"}}]}]`), "db", 1); err != nil {
		t.Errorf("new repo, no backups yet: %v", err)
	}
	if _, err := walKeep([]byte(`[{"name":"other"}]`), "db", 1); err == nil {
		t.Error("missing stanza: want an error")
	}
	keep, err := walKeep([]byte(`[{"name":"db","archive":[{"database":{"repo-key":2},"id":"17-1","min":"000000010000000000000009"}]}]`), "db", 1)
	if err != nil || keep(arch+"0000000100000000/000000010000000000000010-x.gz") {
		t.Errorf("no WAL kept for repo 1: want nothing kept (err %v)", err)
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
