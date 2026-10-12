package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteChunk(t *testing.T) {
	dir := t.TempDir()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	w := func(tid, rel string, idx int, data string, eof, skip bool) error {
		return writeChunk(dir, tid, rel, idx, []byte(data), eof, skip, 0644, 0755)
	}

	// In order: file only appears once the last chunk lands.
	_ = w("t1", "a/x", 0, "he", false, false)
	if got := read("a/x"); got != "<missing>" {
		t.Fatalf("partial file visible: %q", got)
	}
	_ = w("t1", "a/x", 1, "llo", true, false)
	if got := read("a/x"); got != "hello" {
		t.Fatalf("got %q, want hello", got)
	}

	// A missing chunk discards the file instead of writing a corrupt one.
	_ = w("t2", "y", 0, "ab", false, false)
	if err := w("t2", "y", 2, "ef", false, false); err == nil {
		t.Fatal("expected out-of-order error")
	}
	_ = w("t2", "y", 3, "gh", true, false)
	if got := read("y"); got != "<missing>" {
		t.Fatalf("corrupt file written: %q", got)
	}
	if got := read("y.nbc-partial"); got != "<missing>" {
		t.Fatalf("temp file left behind: %q", got)
	}

	// skipExisting leaves an existing file alone.
	_ = w("t3", "a/x", 0, "XX", false, true)
	_ = w("t3", "a/x", 1, "YY", true, true)
	if got := read("a/x"); got != "hello" {
		t.Fatalf("existing file overwritten: %q", got)
	}

	if err := w("t4", "../escape", 0, "z", true, false); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}
