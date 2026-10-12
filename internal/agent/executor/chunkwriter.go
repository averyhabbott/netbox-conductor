package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Per-file progress of in-flight chunked transfers, keyed by transfer ID + path.
// Values: next expected chunk index, or chunkSkip / chunkFailed.
// shortcut: entries of a transfer that never sends its last chunk are never freed
// (bytes per file); fine unless syncs are abandoned many thousands of times.
var (
	chunkMu    sync.Mutex
	chunkState = map[string]int{}
)

const (
	chunkSkip   = -1 // file already present; ignore the rest of its chunks
	chunkFailed = -2 // an earlier chunk failed; ignore the rest, temp file removed
)

// writeChunk appends one chunk of a file transfer to "<dest>.nbc-partial" and
// renames it over dest when the last chunk arrives, so a missing or failed chunk
// never leaves a truncated or corrupt file at dest. Chunks must arrive in order.
// skipExisting leaves an existing dest untouched (pgBackRest repo files are immutable).
func writeChunk(root, transferID, relPath string, idx int, data []byte, eof, skipExisting bool, fileMode, dirMode os.FileMode) error {
	if relPath == "" {
		return nil // end-of-transfer sentinel
	}
	rel := filepath.FromSlash(relPath)
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)+"..") {
		return fmt.Errorf("invalid path: %s", relPath)
	}
	dest := filepath.Join(root, rel)
	tmp := dest + ".nbc-partial"
	key := transferID + "\x00" + relPath

	chunkMu.Lock()
	defer chunkMu.Unlock()
	if eof {
		defer delete(chunkState, key)
	}

	want, seen := chunkState[key]
	if idx == 0 {
		want, seen = 0, true
		if skipExisting {
			if _, err := os.Stat(dest); err == nil {
				want = chunkSkip
			}
		}
	}
	switch {
	case want == chunkSkip || want == chunkFailed:
		chunkState[key] = want
		return nil
	case !seen || want != idx:
		chunkState[key] = chunkFailed
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: got chunk %d, expected %d; file discarded", relPath, idx, want)
	}

	if err := writeChunkData(tmp, idx, data, fileMode, dirMode); err != nil {
		chunkState[key] = chunkFailed
		_ = os.Remove(tmp)
		return err
	}
	if !eof {
		chunkState[key] = idx + 1
		return nil
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("finalising %s: %w", dest, err)
	}
	return nil
}

func writeChunkData(tmp string, idx int, data []byte, fileMode, dirMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(tmp), dirMode); err != nil {
		return fmt.Errorf("creating parent dir for %s: %w", tmp, err)
	}
	flags := os.O_WRONLY | os.O_APPEND
	if idx == 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(tmp, flags, fileMode)
	if err != nil {
		return fmt.Errorf("opening %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	return f.Close()
}
