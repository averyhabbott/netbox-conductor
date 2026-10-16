package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSyncRoundTrip lists a source folder, streams it into a receiver the way
// the conductor relays it, and checks files, accounting and failure handling.
func TestSyncRoundTrip(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	big := bytes.Repeat([]byte("x"), syncChunkSize*2+10) // three chunks
	writeFile(t, filepath.Join(src, "a/big.bin"), big)
	writeFile(t, filepath.Join(src, "empty"), nil)
	if err := os.Symlink(filepath.Join(src, "a"), filepath.Join(src, "latest")); err != nil {
		t.Fatal(err)
	}

	var listed []protocol.SyncFile
	out, err := ListSyncFolder(context.Background(), "s1", src, func(l protocol.SyncListingPayload) error {
		listed = append(listed, l.Files...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var res protocol.SyncListResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.Files != 2 || len(listed) != 2 {
		t.Fatalf("listed %v (result %s), want big.bin and empty only (symlink skipped)", listed, out)
	}

	r := NewSyncReceiver()
	if _, err := r.Start(protocol.SyncReceiveParams{SyncID: "s1", Root: dst, FileMode: 0o640, DirMode: 0o750}, dst); err != nil {
		t.Fatal(err)
	}
	relay := func(c protocol.SyncChunkPayload) error {
		raw, _ := json.Marshal(c) // the real relay marshals too, so Data may be reused
		var got protocol.SyncChunkPayload
		_ = json.Unmarshal(raw, &got)
		switch {
		case got.Path == "empty" && got.EOF:
			got.SHA256 = "tampered"
		case got.Path == "a/big.bin" && got.Index == 0:
			got.Index = 7 // out of order: the file fails once, its other chunks are ignored
		}
		_ = r.Chunk(got)
		return nil
	}
	if _, err := SendSyncFiles(context.Background(), protocol.SyncSendParams{
		SyncID: "s1", Paths: []string{"a/big.bin", "empty", "vanished"},
	}, src, relay); err != nil {
		t.Fatal(err)
	}
	out, err = r.Finish(protocol.SyncFinishParams{SyncID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	var fin protocol.SyncFinishResult
	_ = json.Unmarshal([]byte(out), &fin)
	if fin.Copied != 0 || fin.Skipped != 1 || fin.Errors != 2 ||
		!strings.Contains(fin.Failed[0], "expected 0") || !strings.Contains(fin.Failed[1], "checksum mismatch") {
		t.Errorf("finish = %s, want 1 skipped (vanished), 2 failed (order, checksum)", out)
	}

	// A clean second pass copies both files.
	_, _ = r.Start(protocol.SyncReceiveParams{SyncID: "s2", Root: dst, FileMode: 0o640, DirMode: 0o750}, dst)
	if _, err := SendSyncFiles(context.Background(), protocol.SyncSendParams{SyncID: "s2", Paths: []string{"a/big.bin", "empty"}}, src,
		func(c protocol.SyncChunkPayload) error { return r.Chunk(c) }); err != nil {
		t.Fatal(err)
	}
	if out, _ := r.Finish(protocol.SyncFinishParams{SyncID: "s2"}); !strings.HasPrefix(out, `{"copied":2,`) {
		t.Errorf("second pass = %s, want 2 copied", out)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "a/big.bin")); !bytes.Equal(got, big) {
		t.Error("big.bin content differs")
	}
	if fi, err := os.Stat(filepath.Join(dst, "a/big.bin")); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("big.bin mode = %v, %v; want 0640", fi.Mode(), err)
	}
}

func TestSyncChecksumMismatchLeavesNothing(t *testing.T) {
	dst := t.TempDir()
	r := NewSyncReceiver()
	_, _ = r.Start(protocol.SyncReceiveParams{SyncID: "s", FileMode: 0o644, DirMode: 0o755}, dst)
	_ = r.Chunk(protocol.SyncChunkPayload{SyncID: "s", Path: "empty", EOF: true, SHA256: "tampered"})
	for _, p := range []string{"empty", "empty" + protocol.SyncPartialSuffix} {
		if _, err := os.Stat(filepath.Join(dst, p)); !os.IsNotExist(err) {
			t.Errorf("%s exists after a checksum mismatch", p)
		}
	}
}

func TestSyncRejectsBadPaths(t *testing.T) {
	r := NewSyncReceiver()
	dst := t.TempDir()
	_, _ = r.Start(protocol.SyncReceiveParams{SyncID: "s", FileMode: 0o644, DirMode: 0o755}, dst)
	for _, p := range []string{"../escape", "/etc/passwd", "a/../../b", ""} {
		if err := r.Chunk(protocol.SyncChunkPayload{SyncID: "s", Path: p, EOF: true}); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if _, err := DeleteSyncFiles(context.Background(), protocol.SyncDeleteParams{Paths: []string{"../x"}}, dst); err == nil {
		t.Error("delete of ../x accepted")
	}
}

func TestDeleteSyncFilesPrunesEmptyDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "d1/d2/f"), []byte("1"))
	writeFile(t, filepath.Join(root, "d1/keep"), []byte("2"))
	if _, err := DeleteSyncFiles(context.Background(), protocol.SyncDeleteParams{Paths: []string{"d1/d2/f", "missing"}}, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "d1/d2")); !os.IsNotExist(err) {
		t.Error("empty d1/d2 not removed")
	}
	if _, err := os.Stat(filepath.Join(root, "d1/keep")); err != nil {
		t.Error("d1/keep removed")
	}
}

// Chunks the agent drops (its chunk queue stayed full) fail only their file:
// a gap mid-file is an error; a lost last chunk leaves the file uncounted,
// which the conductor reads as "received N of M files".
func TestSyncDroppedChunks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "mid"), bytes.Repeat([]byte("m"), syncChunkSize*2+10))
	writeFile(t, filepath.Join(src, "ok"), []byte("fine"))
	writeFile(t, filepath.Join(src, "tail"), bytes.Repeat([]byte("t"), syncChunkSize+10))

	r := NewSyncReceiver()
	if _, err := r.Start(protocol.SyncReceiveParams{SyncID: "d", Root: dst, FileMode: 0o640, DirMode: 0o750}, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := SendSyncFiles(context.Background(), protocol.SyncSendParams{SyncID: "d", Paths: []string{"mid", "ok", "tail"}}, src,
		func(c protocol.SyncChunkPayload) error {
			if (c.Path == "mid" && c.Index == 1) || (c.Path == "tail" && c.EOF) {
				return nil // dropped
			}
			_ = r.Chunk(c)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	out, _ := r.Finish(protocol.SyncFinishParams{SyncID: "d"})
	var fin protocol.SyncFinishResult
	_ = json.Unmarshal([]byte(out), &fin)
	if fin.Copied != 1 || fin.Errors != 1 || !strings.Contains(fin.Failed[0], "expected 1") {
		t.Errorf("finish = %s, want ok copied, mid failed (gap), tail uncounted", out)
	}
	for _, name := range []string{"mid", "tail", "tail" + protocol.SyncPartialSuffix} {
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			t.Errorf("%s left behind", name)
		}
	}
}
