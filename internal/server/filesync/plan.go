package filesync

import (
	"cmp"
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

// mirrorPlan makes every other listed node an exact copy of source.
func mirrorPlan(source uuid.UUID, lists map[uuid.UUID][]protocol.SyncFile) plan {
	p := newPlan()
	src := p.index(source, lists[source])
	for node, files := range lists {
		if node == source {
			continue
		}
		have := p.index(node, files)
		for _, f := range src {
			if have[f.Path].SHA256 != f.SHA256 {
				p.copy(source, node, f)
			}
		}
		for path := range have {
			if _, ok := src[path]; !ok {
				p.deletes[node] = append(p.deletes[node], path)
			}
		}
	}
	p.sortDeletes()
	return p
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
