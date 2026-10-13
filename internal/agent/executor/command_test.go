package executor

import (
	"context"
	"testing"
	"time"
)

func TestCommandStopsAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Command(ctx, "sleep", "30").Run(); err == nil {
		t.Fatal("expected an error from a command cut off by its deadline")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("command ran %s past a 200ms deadline", d)
	}
}

func TestSleepReturnsEarlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Minute); err == nil {
		t.Fatal("expected ctx error from a cancelled sleep")
	}
}
