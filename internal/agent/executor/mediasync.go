package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

const defaultChunkSize = 64 * 1024 // 64 KiB

// PushMediaRoot walks mediaRoot and sends each file to the server as media.chunk messages.
// sendFn is called for each Envelope to be sent over the WebSocket connection.
// This runs synchronously and blocks until all files have been sent or an error occurs.
func PushMediaRoot(params protocol.MediaSyncParams, mediaRoot string, sendFn func(protocol.Envelope)) error {
	if mediaRoot == "" {
		return fmt.Errorf("NETBOX_MEDIA_ROOT is not configured")
	}

	chunkSize := params.ChunkSizeB
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	// SourcePath overrides the default MEDIA_ROOT (allows syncing arbitrary directories).
	rootPath := mediaRoot
	if params.SourcePath != "" {
		rootPath = params.SourcePath
	}
	if params.RelativePath != "" {
		rootPath = filepath.Join(mediaRoot, params.RelativePath)
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
		rel, _ := filepath.Rel(mediaRoot, fpath)
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
			sendFn(protocol.Envelope{
				ID:      uuid.New().String(),
				Type:    protocol.TypeMediaChunk,
				Payload: json.RawMessage(payload),
			})

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
	sendFn(protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeMediaChunk,
		Payload: json.RawMessage(payload),
	})

	return nil
}

// WriteMediaChunk writes a received chunk to the target's media root.
// Safe to call concurrently for different files; creates parent directories as needed.
func WriteMediaChunk(chunk protocol.MediaChunkPayload, mediaRoot string) error {
	if chunk.RelativePath == "" {
		return nil // EOF sentinel
	}
	// Guard against path traversal
	rel := filepath.FromSlash(chunk.RelativePath)
	if strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)+"..") {
		return fmt.Errorf("invalid path: %s", chunk.RelativePath)
	}

	dest := filepath.Join(mediaRoot, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("creating parent dir for %s: %w", rel, err)
	}

	var f *os.File
	var err error
	if chunk.ChunkIndex == 0 {
		f, err = os.Create(dest)
	} else {
		f, err = os.OpenFile(dest, os.O_APPEND|os.O_WRONLY, 0644)
	}
	if err != nil {
		return fmt.Errorf("opening %s: %w", dest, err)
	}
	defer f.Close()

	if _, err := f.Write(chunk.Data); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}
