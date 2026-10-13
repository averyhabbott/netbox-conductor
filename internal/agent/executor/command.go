package executor

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// commandStopGrace is how long a cancelled command has to exit after SIGTERM
// before it is killed and its output pipes are closed.
const commandStopGrace = 10 * time.Second

// Command is exec.CommandContext for everything a task runs: when ctx ends
// (task timeout or agent shutdown) the process is stopped and Wait returns.
// It sends SIGTERM rather than exec's default SIGKILL because most task
// commands run under sudo, which forwards SIGTERM to the real command but
// can't forward SIGKILL — a kill would leave the real command running.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = commandStopGrace
	return cmd
}

// sleep pauses for d, returning early with ctx's error if ctx ends first.
// Retry loops use it so a task timeout isn't stuck behind a sleep.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
