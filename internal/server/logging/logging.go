// Package logging provides structured file-based logging for NetBox Conductor.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// AgentLog wraps a per-agent slog.Logger with its underlying file handle.
type AgentLog struct {
	Logger *slog.Logger
	// Path is the absolute path of the underlying log file.
	Path string
	file *os.File
}

// Close flushes and closes the underlying log file.
func (a *AgentLog) Close() error {
	if a.file != nil {
		return a.file.Close()
	}
	return nil
}

// Setup initialises the server-wide structured logger.
// Logs are written to both stdout (captured by journald) and
// <logDir>/<logName>/conductor.log with lumberjack rotation.
// Rotation is controlled by env vars LOG_MAX_SIZE_MB (default 100),
// LOG_MAX_BACKUPS (default 10), LOG_MAX_AGE_DAYS (default 30).
// If the log directory cannot be written (e.g. in dev without root), Setup
// falls back to stdout-only logging rather than refusing to start.
func Setup(logDir, logName, levelStr string) *slog.Logger {
	level := ParseLevel(levelStr)
	dir := filepath.Join(logDir, logName)

	maxSizeMB := envInt("LOG_MAX_SIZE_MB", 100)
	maxBackups := envInt("LOG_MAX_BACKUPS", 10)
	maxAgeDays := envInt("LOG_MAX_AGE_DAYS", 30)

	writers := []io.Writer{os.Stdout}
	if err := os.MkdirAll(dir, 0755); err == nil {
		writers = append(writers, &lumberjack.Logger{
			Filename:   filepath.Join(dir, "conductor.log"),
			MaxSize:    maxSizeMB,
			MaxBackups: maxBackups,
			MaxAge:     maxAgeDays,
			Compress:   true,
		})
	}

	h := slog.NewTextHandler(
		io.MultiWriter(writers...),
		&slog.HandlerOptions{Level: level},
	)
	return slog.New(h)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// OpenAgentLog opens (or creates) the per-agent log file at
// <logDir>/<logName>/<clusterName>/<hostname>.log.
// The returned logger is pre-tagged with "cluster" and "node" attributes
// and its level is always Debug so that heartbeats are captured in the file.
// On any filesystem error, a stdout-backed logger is returned instead of nil.
func OpenAgentLog(logDir, logName, clusterName, hostname string) *AgentLog {
	dir := filepath.Join(logDir, logName, sanitize(clusterName))
	path := filepath.Join(dir, sanitize(hostname)+".log")

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fallbackAgentLog(clusterName, hostname, path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fallbackAgentLog(clusterName, hostname, path)
	}

	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h).With("cluster", clusterName, "node", hostname)
	return &AgentLog{Logger: logger, Path: path, file: f}
}

// AgentLogPath returns the expected log file path for a cluster/node pair
// without opening it. Used by the logs API endpoint.
func AgentLogPath(logDir, logName, clusterName, hostname string) string {
	return filepath.Join(logDir, logName, sanitize(clusterName), sanitize(hostname)+".log")
}

// TailFile returns the last n lines of the file at path.
// Returns an empty slice (not an error) if the file does not exist yet.
func TailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() == 0 {
		return []string{}, nil
	}

	const chunkSize = int64(4096)
	pos := stat.Size()
	var accumulated []byte
	newlineCount := 0

	// Walk backwards in chunks until we have collected enough newlines.
	for pos > 0 && newlineCount <= n {
		toRead := chunkSize
		if pos < toRead {
			toRead = pos
		}
		pos -= toRead
		chunk := make([]byte, toRead)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return nil, err
		}
		accumulated = append(chunk, accumulated...)
		for _, b := range chunk {
			if b == '\n' {
				newlineCount++
			}
		}
	}

	content := strings.TrimRight(string(accumulated), "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// NetboxLogPath returns the on-disk path where forwarded NetBox log lines are stored.
// logFilename is the base name reported by the agent, e.g. "netbox.log".
// Stored as: <logDir>/<logName>/<clusterName>/<hostname>-<logFilename>
func NetboxLogPath(logDir, logName, clusterName, hostname, logFilename string) string {
	return filepath.Join(logDir, logName, sanitize(clusterName), sanitize(hostname)+"-"+sanitize(logFilename))
}

// ListNetboxLogNames returns the netbox log names that have been stored for a node.
// Each name is the portion after the "<hostname>-" prefix, e.g. "netbox.log", "django.log".
func ListNetboxLogNames(logDir, logName, clusterName, hostname string) ([]string, error) {
	dir := filepath.Join(logDir, logName, sanitize(clusterName))
	prefix := sanitize(hostname) + "-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		logFile := strings.TrimPrefix(name, prefix)
		if logFile != "" {
			names = append(names, logFile)
		}
	}
	return names, nil
}

// AppendLines appends lines to a file, creating it and its parent directories if needed.
func AppendLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := fmt.Fprintln(f, line); err != nil {
			return err
		}
	}
	return nil
}

// ParseLevel converts a log level string to slog.Level.
// Unknown values default to Info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func fallbackAgentLog(clusterName, hostname, path string) *AgentLog {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h).With("cluster", clusterName, "node", hostname)
	return &AgentLog{Logger: logger, Path: path}
}

// sanitize replaces characters that are unsafe in file/directory names.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, s)
}
