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

const defaultChunkSize = 64 * 1024 // 64 KiB

// PushMediaRoot walks mediaRoot and sends each file to the server as media.chunk messages.
// sendFn is called for each Envelope to be sent over the WebSocket connection.
// This runs synchronously and blocks until all files have been sent or an error occurs.
func PushMediaRoot(params protocol.MediaSyncParams, mediaRoot string, sendFn func(protocol.Envelope) error) error {
	if mediaRoot == "" {
		return fmt.Errorf("NETBOX_MEDIA_ROOT is not configured")
	}

	chunkSize := params.ChunkSizeB
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	// SourcePath overrides the default MEDIA_ROOT (allows syncing arbitrary directories).
	// relBase is what sent paths are relative to: files from SourcePath land at the
	// target's media root; files under RelativePath keep that prefix.
	rootPath, relBase := mediaRoot, mediaRoot
	if params.SourcePath != "" {
		rootPath, relBase = params.SourcePath, params.SourcePath
	}
	if params.RelativePath != "" {
		rootPath, relBase = filepath.Join(mediaRoot, params.RelativePath), mediaRoot
	}

	// Collect all files first so we know total counts (for progress reporting).
	var files []string
	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking media root: %w", err)
	}

	buf := make([]byte, chunkSize)

	for _, fpath := range files {
		rel, _ := filepath.Rel(relBase, fpath)
		rel = filepath.ToSlash(rel)

		f, err := os.Open(fpath)
		if err != nil {
			// Skip unreadable files — log but continue
			continue
		}

		chunkIdx := 0
		for {
			n, readErr := io.ReadFull(f, buf)
			data := buf[:n]
			isEOF := readErr == io.EOF || readErr == io.ErrUnexpectedEOF

			chunk := protocol.MediaChunkPayload{
				TransferID:   params.TransferID,
				RelativePath: rel,
				ChunkIndex:   chunkIdx,
				Data:         append([]byte(nil), data...),
				EOF:          isEOF,
			}
			payload, _ := json.Marshal(chunk)
			if err := sendFn(protocol.Envelope{
				ID:      uuid.New().String(),
				Type:    protocol.TypeMediaChunk,
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

	// Send a final EOF sentinel with empty path to signal end-of-transfer.
	done := protocol.MediaChunkPayload{
		TransferID:   params.TransferID,
		RelativePath: "",
		ChunkIndex:   0,
		EOF:          true,
	}
	payload, _ := json.Marshal(done)
	return sendFn(protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeMediaChunk,
		Payload: json.RawMessage(payload),
	})
}

// WriteMediaChunk writes a received chunk to the target's media root.
// Chunks must arrive in order; each file appears only once complete.
func WriteMediaChunk(chunk protocol.MediaChunkPayload, mediaRoot string) error {
	return writeChunk(mediaRoot, chunk.TransferID, chunk.RelativePath, chunk.ChunkIndex, chunk.Data, chunk.EOF, false, 0644, 0755)
}
