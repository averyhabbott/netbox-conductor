package executor

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoReceive(t *testing.T) {
	root := t.TempDir()
	sum := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	in := "d 3 a/one\nabcd 2 a/one\ndee " + sum("abcde") + " a/one\n" + // two pieces, committed
		"d 2 b/two\nxye " + sum("other") + " b/two\n" + // checksum mismatch
		"d 1 three\nza three\n" + // abandoned
		"e " + sum("") + " ../escape\n" // no data, bad path
	var out bytes.Buffer
	if err := repoReceive(root, bufio.NewReader(strings.NewReader(in)), &out); err != nil {
		t.Fatal(err)
	}
	replies := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(replies) != 3 || replies[0] != "ok" || !strings.HasPrefix(replies[1], "err checksum") || !strings.HasPrefix(replies[2], "err") {
		t.Errorf("replies = %q", replies)
	}
	if got, err := os.ReadFile(filepath.Join(root, "a/one")); err != nil || string(got) != "abcde" {
		t.Errorf("a/one = %q, %v", got, err)
	}
	var left []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			left = append(left, strings.TrimPrefix(p, root+"/"))
		}
		return nil
	})
	if len(left) != 1 {
		t.Errorf("files left = %v, want only a/one", left)
	}
}
