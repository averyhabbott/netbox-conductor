package executor

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// DiscoverLogFiles parses NetBox's configuration.py, finds the LOGGING section,
// and returns a map of log name (base filename) → absolute file path for every
// file-based handler (RotatingFileHandler, FileHandler, TimedRotatingFileHandler, …).
// Returns nil if the config cannot be read or contains no file-handler entries.
func DiscoverLogFiles(configPath string) map[string]string {
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		slog.Debug("netbox log discovery: cannot read config", "path", configPath, "error", err)
		return nil
	}

	content := string(data)

	// Locate the LOGGING assignment so we only look inside that section.
	start := findLoggingSection(content)
	if start < 0 {
		return nil
	}

	// Match 'filename': '/abs/path' or "filename": "/abs/path" (both quote styles).
	re := regexp.MustCompile(`["']filename["']\s*:\s*["']([^"']+)["']`)
	result := make(map[string]string)

	for _, m := range re.FindAllStringSubmatch(content[start:], -1) {
		absPath := m[1]
		if !filepath.IsAbs(absPath) {
			continue // skip relative paths — they're ambiguous outside the agent's cwd
		}
		logName := filepath.Base(absPath)
		if logName == "" || logName == "." {
			continue
		}
		result[logName] = absPath
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// findLoggingSection returns the index of the "LOGGING = {" assignment in content,
// or -1 if not found.
func findLoggingSection(content string) int {
	re := regexp.MustCompile(`\bLOGGING\s*=\s*\{`)
	loc := re.FindStringIndex(content)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// TailNetboxLogs discovers log files from NetBox's configuration.py and tails each
// one concurrently. sendFn is called with (logName, lines) for every batch of new
// lines. logName is the base filename (e.g. "netbox.log").
//
// If no files are discovered via configuration.py and fallbackPath is non-empty,
// falls back to tailing that single file under filepath.Base(fallbackPath).
//
// Blocks until ctx is cancelled.
func TailNetboxLogs(ctx context.Context, configPath, fallbackPath string, sendFn func(logName string, lines []string)) {
	files := DiscoverLogFiles(configPath)

	if len(files) == 0 && fallbackPath != "" {
		files = map[string]string{
			filepath.Base(fallbackPath): fallbackPath,
		}
	}
	if len(files) == 0 {
		return
	}

	var wg sync.WaitGroup
	for logName, logPath := range files {
		logName, logPath := logName, logPath // pin loop variables
		wg.Add(1)
		go func() {
			defer wg.Done()
			tailOne(ctx, logName, logPath, sendFn)
		}()
	}
	wg.Wait()
}

func tailOne(ctx context.Context, logName, logPath string, sendFn func(logName string, lines []string)) {
	const pollInterval = 5 * time.Second

	var offset int64

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	slog.Info("netbox log tail started", "name", logName, "path", logPath)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			lines, newOffset, err := readNewLines(logPath, offset)
			if err != nil {
				slog.Debug("netbox log tail error", "name", logName, "path", logPath, "error", err)
				continue
			}
			offset = newOffset
			if len(lines) > 0 {
				sendFn(logName, lines)
			}
		}
	}
}

func readNewLines(path string, offset int64) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}

	// File was rotated/truncated — seek back to start.
	if stat.Size() < offset {
		offset = 0
	}

	if stat.Size() == offset {
		return nil, offset, nil
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, offset, err
	}

	newOffset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, offset, err
	}

	return lines, newOffset, nil
}
