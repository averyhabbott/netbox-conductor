// Package scheduler contains background goroutines that maintain system health.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
)

const (
	// SentTimeout is how long a task can stay in "sent" (no ack) before timing out.
	SentTimeout = 5 * time.Minute
	// AckTimeout is how long a task can stay in "ack" (no result) before timing out.
	AckTimeout = 10 * time.Minute

	sweepInterval = time.Minute
)

// TaskSweeper periodically times out stuck tasks.
type TaskSweeper struct {
	tasks *queries.TaskResultQuerier
}

func NewTaskSweeper(tasks *queries.TaskResultQuerier) *TaskSweeper {
	return &TaskSweeper{tasks: tasks}
}

// Run sweeps for stale tasks on a regular interval until ctx is cancelled.
func (s *TaskSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.tasks.TimeoutStale(ctx, SentTimeout, AckTimeout)
			if err != nil {
				slog.Warn("task sweeper error", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("timed out stale tasks", "count", n,
					"sent_timeout", SentTimeout.String(),
					"ack_timeout", AckTimeout.String())
			}
		}
	}
}
