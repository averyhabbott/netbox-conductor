package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, "new", 0640, -1); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("content = %q, %v; want %q", data, err, "new")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0640 {
		t.Errorf("mode = %v, want 0640", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestCheckSHA256(t *testing.T) {
	const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got, err := checkSHA256("hello", ""); err != nil || got != helloSHA {
		t.Errorf("no expected sum: got %q, %v", got, err)
	}
	if _, err := checkSHA256("hello", helloSHA); err != nil {
		t.Errorf("matching sum: %v", err)
	}
	if _, err := checkSHA256("hello", "deadbeef"); err == nil {
		t.Error("mismatched sum: want error")
	}
}
