package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// Folder sync, agent side. The conductor lists the folder on every node,
// works out what each destination needs, asks the source to stream those
// files and forwards each chunk to the destinations that need it.

const (
	syncChunkSize    = 64 * 1024 // bytes of file data per sync.chunk
	syncListingBytes = 1 << 20   // flush a sync.listing piece at ~1 MB (messages are capped at 4 MB)
	maxFailedReport  = 20        // failures named in a sync.finish result
)

// syncRelPath checks a conductor-supplied relative path and returns it joined to root.
func syncRelPath(root, relPath string) (string, error) {
	rel := filepath.FromSlash(relPath)
	if relPath == "" || filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("invalid path: %q", relPath)
	}
	return filepath.Join(root, rel), nil
}

// ListSyncFolder hashes every regular file under root and sends the listing to
// the conductor in pieces through send. Symlinks are skipped, never followed.
// A missing root is an error; the conductor also refuses an empty source.
func ListSyncFolder(ctx context.Context, syncID, root string, send func(protocol.SyncListingPayload) error) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return "", fmt.Errorf("sync folder %s: %w", root, err)
	}
	piece := protocol.SyncListingPayload{SyncID: syncID}
	pieceBytes, total := 0, 0
	flush := func() error {
		if len(piece.Files) == 0 {
			return nil
		}
		err := send(piece)
		piece.Files, pieceBytes = nil, 0
		return err
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum, size, err := hashFile(path)
		if err != nil {
			return err
		}
		f := protocol.SyncFile{Path: filepath.ToSlash(rel), Size: size, SHA256: sum, MTime: info.ModTime().UnixNano()}
		piece.Files = append(piece.Files, f)
		pieceBytes += len(f.Path) + 150 // JSON keys, checksum, size, mtime
		total++
		if pieceBytes >= syncListingBytes {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if err != nil {
		return "", fmt.Errorf("listing %s: %w", root, err)
	}
	out, _ := json.Marshal(protocol.SyncListResult{Files: total, FreeBytes: st.Bavail * uint64(st.Bsize)})
	return string(out), nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// SendSyncFiles streams each named file under root as sync.chunk messages.
// A file that vanished since the listing is sent as Gone, an unreadable one
// as Err, so destinations can account for every file.
func SendSyncFiles(ctx context.Context, p protocol.SyncSendParams, root string, send func(protocol.SyncChunkPayload) error) (string, error) {
	buf := make([]byte, syncChunkSize)
	for _, rel := range p.Paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		path, err := syncRelPath(root, rel)
		if err != nil {
			return "", err
		}
		if err := sendSyncFile(p.SyncID, rel, path, buf, send); err != nil {
			return "", fmt.Errorf("sending %s: %w", rel, err)
		}
	}
	return fmt.Sprintf("sent %d files", len(p.Paths)), nil
}

// sendSyncFile streams one file. Only send errors are returned: read problems
// are reported to the destinations in the stream itself.
func sendSyncFile(syncID, rel, path string, buf []byte, send func(protocol.SyncChunkPayload) error) error {
	end := protocol.SyncChunkPayload{SyncID: syncID, Path: rel, EOF: true}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			end.Gone = true
		} else {
			end.Err = err.Error()
		}
		return send(end)
	}
	defer f.Close()
	h := sha256.New()
	for idx := 0; ; idx++ {
		n, readErr := io.ReadFull(f, buf)
		h.Write(buf[:n])
		c := protocol.SyncChunkPayload{SyncID: syncID, Path: rel, Index: idx, Data: buf[:n]}
		switch readErr {
		case nil:
		case io.EOF, io.ErrUnexpectedEOF:
			c.EOF, c.SHA256 = true, hex.EncodeToString(h.Sum(nil))
		default:
			end.Index, end.Err = idx, readErr.Error()
			return send(end)
		}
		// send marshals c before returning, so buf can be reused.
		if err := send(c); err != nil || c.EOF {
			return err
		}
	}
}

// SyncReceiver writes incoming sync.chunk messages for each active sync.
// Its methods must all run on one goroutine (the agent's chunk queue), which
// also keeps chunks, sync.receive and sync.finish in arrival order.
type SyncReceiver struct {
	syncs map[string]*syncRecv
}

type syncRecv struct {
	root              string
	fileMode, dirMode os.FileMode
	repo              *repoReceiver // a pgBackRest repo: files are written by the repo helper
	result            protocol.SyncFinishResult

	skip        string // a file that already failed; its remaining chunks are ignored
	repairTried bool   // access to the folder was repaired once this sync

	// file being written
	path string
	tmp  partialFile
	hash hash.Hash
	next int
}

// partialFile is a received file being written as "<dest>.nbc-partial":
// commit renames it into place, abort discards it.
type partialFile interface {
	io.Writer
	commit() error
	abort()
}

// localPartial is a partial file the agent writes itself.
type localPartial struct {
	f    *os.File
	dest string
}

func (p *localPartial) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *localPartial) commit() error {
	err := p.f.Close()
	if err == nil {
		err = os.Rename(p.f.Name(), p.dest)
	}
	if err != nil {
		_ = os.Remove(p.f.Name())
	}
	return err
}

func (p *localPartial) abort() {
	_ = p.f.Close()
	_ = os.Remove(p.f.Name())
}

func NewSyncReceiver() *SyncReceiver {
	return &SyncReceiver{syncs: map[string]*syncRecv{}}
}

// Start begins accepting chunks for a sync. A sync of the same folder that
// was never finished (the conductor lost track of it) is stopped first.
func (r *SyncReceiver) Start(p protocol.SyncReceiveParams, root string) (string, error) {
	if p.SyncID == "" || root == "" {
		return "", errors.New("sync_id and root are required")
	}
	for id, s := range r.syncs {
		if s.root == root {
			s.stop()
			delete(r.syncs, id)
		}
	}
	s := &syncRecv{root: root, fileMode: os.FileMode(p.FileMode), dirMode: os.FileMode(p.DirMode)}
	if p.AsPostgres {
		var err error
		if s.repo, err = startRepoReceiver(root); err != nil {
			return "", err
		}
	}
	r.syncs[p.SyncID] = s
	return "receiving", nil
}

// Chunk handles one chunk. Problems are recorded against the file and reported
// by Finish; the returned error is only for logging.
func (r *SyncReceiver) Chunk(c protocol.SyncChunkPayload) error {
	s := r.syncs[c.SyncID]
	if s == nil {
		return fmt.Errorf("chunk for unknown sync %s", c.SyncID)
	}
	if s.skip != "" && c.Path == s.skip {
		if c.EOF {
			s.skip = ""
		}
		return nil
	}
	if s.tmp != nil && (c.Index == 0 || c.Path != s.path) {
		s.abort(s.path + ": incomplete") // a new file started before this one ended
	}
	err := s.chunk(c)
	if err != nil {
		s.abort(c.Path + ": " + err.Error())
		if !c.EOF {
			s.skip = c.Path
		}
	}
	return err
}

func (s *syncRecv) chunk(c protocol.SyncChunkPayload) error {
	switch {
	case c.Gone:
		s.result.Skipped++
		return nil
	case c.Err != "":
		return fmt.Errorf("source: %s", c.Err)
	case c.Index != s.next || (c.Index > 0 && s.tmp == nil):
		return fmt.Errorf("got chunk %d, expected %d", c.Index, s.next)
	}
	if c.Index == 0 {
		dest, err := syncRelPath(s.root, c.Path)
		if err != nil {
			return err
		}
		if s.repo != nil {
			p, err := s.repo.open(c.Path)
			if err != nil {
				return err
			}
			s.tmp, s.hash, s.next = p, sha256.New(), 0
		} else if err := withAccessRepair(context.Background(), s.root, &s.repairTried, func() error { return s.open(dest) }); err != nil {
			return err
		}
		s.path = c.Path
	}
	if _, err := s.tmp.Write(c.Data); err != nil {
		return err
	}
	s.hash.Write(c.Data)
	s.next++
	if !c.EOF {
		return nil
	}
	if got := hex.EncodeToString(s.hash.Sum(nil)); got != c.SHA256 {
		return fmt.Errorf("checksum mismatch (got %s, source sent %s)", got, c.SHA256)
	}
	tmp := s.tmp
	s.tmp = nil
	if err := tmp.commit(); err != nil {
		return err
	}
	s.path, s.next = "", 0
	s.result.Copied++
	return nil
}

// open creates "<dest>.nbc-partial" and any missing parent dirs with the
// sync's modes. Explicit chmods because the agent's umask would strip group write.
func (s *syncRecv) open(dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, s.dirMode); err != nil {
		return err
	}
	// Dirs the agent doesn't own fail with EPERM and keep their modes.
	for d := dir; len(d) > len(s.root); d = filepath.Dir(d) {
		_ = os.Chmod(d, s.dirMode)
	}
	f, err := os.OpenFile(dest+protocol.SyncPartialSuffix, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, s.fileMode)
	if err != nil {
		return err
	}
	_ = f.Chmod(s.fileMode)
	s.tmp, s.hash, s.next = &localPartial{f: f, dest: dest}, sha256.New(), 0
	return nil
}

// abort discards the file being written and, with a reason, records a failure.
func (s *syncRecv) abort(reason string) {
	if s.tmp != nil {
		s.tmp.abort()
		s.tmp = nil
	}
	s.path, s.next = "", 0
	if reason != "" {
		s.result.Errors++
		if len(s.result.Failed) < maxFailedReport {
			s.result.Failed = append(s.result.Failed, reason)
		}
	}
}

// Finish stops accepting chunks for a sync and reports what arrived. A file
// still unfinished (its sender was cut off) is discarded.
func (r *SyncReceiver) Finish(p protocol.SyncFinishParams) (string, error) {
	s := r.syncs[p.SyncID]
	if s == nil {
		return "", fmt.Errorf("unknown sync %s", p.SyncID)
	}
	delete(r.syncs, p.SyncID)
	s.abort("")
	if s.repo != nil {
		if err := s.repo.close(); err != nil {
			s.abort(err.Error())
		}
	}
	out, _ := json.Marshal(s.result)
	return string(out), nil
}

// StopAll stops every sync, discarding unfinished files: used when the
// connection to the conductor drops, which ends those syncs.
func (r *SyncReceiver) StopAll() {
	for id, s := range r.syncs {
		s.stop()
		delete(r.syncs, id)
	}
}

// stop discards the file being written and ends the repo helper, if any.
func (s *syncRecv) stop() {
	s.abort("")
	if s.repo != nil {
		s.repo.stop()
	}
}

// DeleteSyncFiles removes the named files under root (in a repo, through the
// repo helper as postgres), then any directories that became empty.
func DeleteSyncFiles(ctx context.Context, p protocol.SyncDeleteParams, root string) (string, error) {
	var err error
	if p.AsPostgres {
		err = runRepoHelper(ctx, strings.Join(p.Paths, "\n"), "delete", root)
	} else {
		var repairTried bool
		err = removeSyncFiles(root, p.Paths, func(path string) error {
			return withAccessRepair(ctx, root, &repairTried, func() error { return os.Remove(path) })
		})
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted %d files", len(p.Paths)), nil
}

// removeSyncFiles removes each named file under root with remove, then any
// directories that became empty (never root itself). Already-missing files are fine.
func removeSyncFiles(root string, rels []string, remove func(path string) error) error {
	var failed []string
	for _, rel := range rels {
		path, err := syncRelPath(root, rel)
		if err == nil {
			err = remove(path)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = append(failed, err.Error())
			continue
		}
		for d := filepath.Dir(path); len(d) > len(root) && os.Remove(d) == nil; d = filepath.Dir(d) {
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d deletes failed, first: %s", len(failed), len(rels), failed[0])
	}
	return nil
}
