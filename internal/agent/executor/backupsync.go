package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

const (
	defaultBackupRepoPath  = "/var/lib/pgbackrest"
	defaultBackupChunkSize = 64 * 1024 // 64 KiB
)

// PushBackupRepo walks the local pgBackRest repository and sends all files to the
// conductor relay via backup.chunk messages. The write side skips files that already
// exist at the destination — pgBackRest repo files are immutable once written, so
// file existence implies identical content.
//
// sendFn is called for each Envelope to be sent over the WebSocket connection.
// This mirrors PushMediaRoot from mediasync.go.
func PushBackupRepo(params protocol.BackupSyncReadParams, sendFn func(protocol.Envelope) error) error {
	repoPath := params.RepoPath
	if repoPath == "" {
		repoPath = defaultBackupRepoPath
	}

	if _, err := os.Stat(repoPath); err != nil {
		return fmt.Errorf("pgBackRest repo not found at %s: %w", repoPath, err)
	}

	chunkSize := params.ChunkSizeB
	if chunkSize <= 0 {
		chunkSize = defaultBackupChunkSize
	}

	var files []string
	err := filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking pgBackRest repo: %w", err)
	}

	buf := make([]byte, chunkSize)

	for _, fpath := range files {
		rel, _ := filepath.Rel(repoPath, fpath)
		rel = filepath.ToSlash(rel)

		f, err := os.Open(fpath)
		if err != nil {
			continue // skip unreadable files
		}

		chunkIdx := 0
		for {
			n, readErr := io.ReadFull(f, buf)
			data := buf[:n]
			isEOF := readErr == io.EOF || readErr == io.ErrUnexpectedEOF

			chunk := protocol.BackupChunkPayload{
				TransferID:   params.TransferID,
				RelativePath: rel,
				ChunkIndex:   chunkIdx,
				Data:         append([]byte(nil), data...),
				EOF:          isEOF,
			}
			payload, _ := json.Marshal(chunk)
			if err := sendFn(protocol.Envelope{
				ID:      uuid.New().String(),
				Type:    protocol.TypeBackupChunk,
				Payload: json.RawMessage(payload),
			}); err != nil {
				_ = f.Close()
				return fmt.Errorf("sending %s: %w", rel, err)
			}

			chunkIdx++
			if isEOF {
				break
			}
			if readErr != nil {
				_ = f.Close()
				return fmt.Errorf("reading %s: %w", rel, readErr)
			}
		}
		_ = f.Close()
	}

	// EOF sentinel signals end of transfer.
	done := protocol.BackupChunkPayload{
		TransferID:   params.TransferID,
		RelativePath: "",
		EOF:          true,
	}
	payload, _ := json.Marshal(done)
	return sendFn(protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeBackupChunk,
		Payload: json.RawMessage(payload),
	})
}

// WriteBackupChunk writes a received pgBackRest repo chunk to the local repo directory.
// Skips files that already exist — pgBackRest repo files are immutable once written,
// so existence implies identical content and no overwrite is needed.
func WriteBackupChunk(chunk protocol.BackupChunkPayload, repoPath string) error {
	if repoPath == "" {
		repoPath = defaultBackupRepoPath
	}
	return writeChunk(repoPath, chunk.TransferID, chunk.RelativePath, chunk.ChunkIndex, chunk.Data, chunk.EOF, true, 0640, 0750)
}
