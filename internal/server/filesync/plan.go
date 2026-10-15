package filesync

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

// batchBytes caps the path bytes in one sync.send or sync.delete; agent↔conductor
// messages are capped at 4 MB.
const batchBytes = 1 << 20

// plan is what one sync does: which node sends each file to which others, and
// what each node deletes.
type plan struct {
	sends   map[uuid.UUID]map[string][]uuid.UUID // sending node → path → receiving nodes
	copies  map[uuid.UUID]int                    // receiving node → files it gets
	bytes   map[uuid.UUID]int64                  // receiving node → bytes it gets
	deletes map[uuid.UUID][]string

	// Two-way only.
	files     map[string]string // path → SHA-256 every node holds afterwards
	deleted   []string          // paths deleted on every node
	conflicts []string          // paths changed on several nodes, or changed on one and deleted on another
}

func newPlan() plan {
	return plan{
		sends:   map[uuid.UUID]map[string][]uuid.UUID{},
		copies:  map[uuid.UUID]int{},
		bytes:   map[uuid.UUID]int64{},
		deletes: map[uuid.UUID][]string{},
		files:   map[string]string{},
	}
}

func (p *plan) copy(from, to uuid.UUID, f protocol.SyncFile) {
	if p.sends[from] == nil {
		p.sends[from] = map[string][]uuid.UUID{}
	}
	p.sends[from][f.Path] = append(p.sends[from][f.Path], to)
	p.copies[to]++
	p.bytes[to] += f.Size
}

// index maps a listing by path. Leftover partial files are deleted, never synced.
func (p *plan) index(node uuid.UUID, files []protocol.SyncFile) map[string]protocol.SyncFile {
	m := make(map[string]protocol.SyncFile, len(files))
	for _, f := range files {
		if strings.HasSuffix(f.Path, protocol.SyncPartialSuffix) {
			p.deletes[node] = append(p.deletes[node], f.Path)
			continue
		}
		m[f.Path] = f
	}
	return m
}

// backupPlan syncs a pgBackRest repo from the main server (source) in one run:
//   - gather: WAL only other nodes hold (archived while they were primary) is
//     copied to the main server, and every other node, if keep says the main
//     server's retention still covers it; .history files always are. It never
//     overwrites the main server's files.
//   - spread: every other node becomes an exact copy of the main server's repo
//     plus what was gathered. WAL found only elsewhere and older than the
//     main server's retention has been expired by pgBackRest and is deleted.
//
// A WAL segment held with different contents (split brain) is a conflict: no
// node's copy of it is changed or deleted, and it isn't gathered.
func backupPlan(source uuid.UUID, lists map[uuid.UUID][]protocol.SyncFile, keep func(path string) bool) plan {
	p := newPlan()
	nodes := slices.SortedFunc(maps.Keys(lists), func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	have := make(map[uuid.UUID]map[string]protocol.SyncFile, len(lists))
	versions := map[string]map[string]bool{} // segment → path + checksum of each copy
	holds := map[uuid.UUID]map[string]bool{} // node → segments it holds
	for _, node := range nodes {
		have[node] = p.index(node, lists[node])
		holds[node] = map[string]bool{}
		for path, f := range have[node] {
			if isWAL(path) {
				seg := walSegment(path)
				if versions[seg] == nil {
					versions[seg] = map[string]bool{}
				}
				versions[seg][path+" "+f.SHA256] = true
				holds[node][seg] = true
			}
		}
	}
	conflicted := map[string]bool{}
	for seg, v := range versions {
		if len(v) > 1 {
			conflicted[seg] = true
			p.conflicts = append(p.conflicts, seg)
		}
	}

	want := map[string]version{}
	for path, f := range have[source] {
		want[path] = version{source, f}
	}
	for _, node := range nodes {
		for path, f := range have[node] {
			if _, ok := want[path]; ok || !isWAL(path) || conflicted[walSegment(path)] {
				continue
			}
			if strings.HasSuffix(path, ".history") || keep(path) {
				want[path] = version{node, f}
			}
		}
	}

	for _, node := range nodes {
		for path, v := range want {
			if have[node][path].SHA256 != v.file.SHA256 && !(isWAL(path) && holds[node][walSegment(path)] && conflicted[walSegment(path)]) {
				p.copy(v.node, node, v.file)
			}
		}
		if node == source {
			continue
		}
		for path := range have[node] {
			if _, ok := want[path]; !ok && !(isWAL(path) && conflicted[walSegment(path)]) {
				p.deletes[node] = append(p.deletes[node], path)
			}
		}
	}
	p.sortDeletes()
	slices.Sort(p.conflicts)
	return p
}

// isWAL reports a file in a repo's WAL archive: a segment, partial segment or
// timeline .history file (archive/<stanza>/<archive id>/...).
func isWAL(p string) bool {
	return strings.HasPrefix(p, "archive/") && strings.Count(p, "/") >= 3 && !isIndexFile(p)
}

// walSegment names what a WAL file holds: its path up to the "-<checksum>"
// pgBackRest appends to segments ("…/000000080000000000000005",
// "…/000000080000000000000005.partial"); a .history file's own path.
func walSegment(p string) string {
	dir, base := path.Split(p)
	base, _, _ = strings.Cut(base, "-")
	return dir + base
}

// walKeep reads `pgbackrest info --output=json` from the main server and
// returns whether a WAL segment in repo's archive is at or after the oldest
// WAL the repo keeps for its archive id, i.e. within retention. Timelines are
// ignored: only the position counts.
func walKeep(info []byte, stanza string, repo int) (func(path string) bool, error) {
	var stanzas []struct {
		Name    string `json:"name"`
		Archive []struct {
			Database struct {
				RepoKey int `json:"repo-key"`
			} `json:"database"`
			ID  string `json:"id"`
			Min string `json:"min"`
		} `json:"archive"`
		Repo []struct {
			Key    int `json:"key"`
			Status struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"status"`
		} `json:"repo"`
	}
	if err := json.Unmarshal(info, &stanzas); err != nil {
		return nil, fmt.Errorf("reading pgbackrest info: %w", err)
	}
	for _, st := range stanzas {
		if st.Name != stanza {
			continue
		}
		for _, r := range st.Repo {
			if r.Key == repo && !protocol.PGBackRestRepoReadable(r.Status.Code) {
				return nil, fmt.Errorf("pgbackrest info: repo%d: %s", repo, r.Status.Message)
			}
		}
		oldest := map[string]string{} // archive id → position of its oldest kept segment
		for _, a := range st.Archive {
			if a.Database.RepoKey == repo && len(a.Min) == 24 {
				oldest[a.ID] = a.Min[8:]
			}
		}
		return func(p string) bool {
			parts := strings.Split(p, "/")
			seg := parts[len(parts)-1]
			min, ok := oldest[parts[2]]
			return ok && len(seg) >= 24 && seg[8:24] >= min
		}, nil
	}
	return nil, fmt.Errorf("pgbackrest info: no stanza %q", stanza)
}

// version is one node's copy of a file.
type version struct {
	node uuid.UUID
	file protocol.SyncFile
}

// twoWayPlan merges the listed nodes, comparing each with its record from its
// last successful sync:
//   - changed (a version no node has on record, on a node with a record): the
//     newest changed version goes everywhere; it also beats a delete;
//   - deleted (on record but gone, or a delete marker): deleted everywhere;
//   - otherwise the version on the node that synced most recently goes
//     everywhere, so a node that missed syncs catches up;
//   - a file only nodes without a record have (their first sync) is copied,
//     newest version first. Their first sync never causes deletes elsewhere.
func twoWayPlan(lists map[uuid.UUID][]protocol.SyncFile, rec queries.FileSyncRecord) plan {
	p := newPlan()
	have := make(map[uuid.UUID]map[string]protocol.SyncFile, len(lists))
	paths := map[string]bool{}
	for node, files := range lists {
		have[node] = p.index(node, files)
		for path := range have[node] {
			paths[path] = true
		}
		for path := range rec.Files[node] {
			paths[path] = true
		}
	}
	for path := range rec.Deleted {
		paths[path] = true
	}

	for path := range paths {
		known := map[string]bool{} // versions some node had at its last sync
		for _, files := range rec.Files {
			if sum, ok := files[path]; ok {
				known[sum] = true
			}
		}
		deleted := rec.Deleted[path]
		var changed, kept, fresh []version
		for node := range lists {
			f, present := have[node][path]
			old, recorded := rec.Files[node]
			_, wasThere := old[path]
			switch {
			case !recorded:
				if present {
					fresh = append(fresh, version{node, f})
				}
			case present && known[f.SHA256]:
				kept = append(kept, version{node, f})
			case present:
				changed = append(changed, version{node, f})
			case wasThere:
				deleted = true
			}
		}

		var win version
		switch {
		case len(changed) > 0:
			win = slices.MaxFunc(changed, newer)
			if deleted || differ(changed) {
				p.conflicts = append(p.conflicts, path)
			}
		case deleted:
			p.deleted = append(p.deleted, path)
			for node := range lists {
				if _, ok := have[node][path]; ok {
					p.deletes[node] = append(p.deletes[node], path)
				}
			}
			continue
		case len(kept) > 0:
			win = slices.MaxFunc(kept, func(a, b version) int {
				return cmp.Or(rec.SyncedAt[a.node].Compare(rec.SyncedAt[b.node]), newer(a, b))
			})
		case len(fresh) > 0:
			win = slices.MaxFunc(fresh, newer)
			if differ(fresh) {
				p.conflicts = append(p.conflicts, path)
			}
		default:
			continue // on record only for nodes that aren't listed this time
		}

		p.files[path] = win.file.SHA256
		for node := range lists {
			if have[node][path].SHA256 != win.file.SHA256 {
				p.copy(win.node, node, win.file)
			}
		}
	}
	p.sortDeletes()
	slices.Sort(p.deleted)
	slices.Sort(p.conflicts)
	return p
}

// newer orders versions by modification time; ties are broken by checksum,
// then node, so every run picks the same one.
func newer(a, b version) int {
	return cmp.Or(cmp.Compare(a.file.MTime, b.file.MTime),
		strings.Compare(a.file.SHA256, b.file.SHA256),
		strings.Compare(a.node.String(), b.node.String()))
}

// differ reports whether vs hold more than one distinct content.
func differ(vs []version) bool {
	for _, v := range vs[1:] {
		if v.file.SHA256 != vs[0].file.SHA256 {
			return true
		}
	}
	return false
}

func (p *plan) sortDeletes() {
	for _, d := range p.deletes {
		slices.Sort(d)
	}
}

// isIndexFile reports pgBackRest's index files, which name the backups and
// WAL in the repo; they are sent last so a destination never lists files it
// doesn't have yet.
func isIndexFile(p string) bool {
	switch path.Base(p) {
	case "backup.info", "backup.info.copy", "archive.info", "archive.info.copy":
		return true
	}
	return false
}

// sendOrder returns the paths of routes sorted, index files last.
func sendOrder(routes map[string][]uuid.UUID) []string {
	paths := make([]string, 0, len(routes))
	for p := range routes {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, func(a, b string) int {
		if ia, ib := isIndexFile(a), isIndexFile(b); ia != ib {
			if ia {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	return paths
}

// batches splits paths, in order, into groups of at most batchBytes of path text.
func batches(paths []string) [][]string {
	var out [][]string
	size := 0
	for i, p := range paths {
		if i == 0 || size+len(p) > batchBytes {
			out = append(out, nil)
			size = 0
		}
		out[len(out)-1] = append(out[len(out)-1], p)
		size += len(p) + 4 // JSON quotes and comma
	}
	return out
}
