package executor

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

func TestPgbackrestTargetTime(t *testing.T) {
	cases := map[string]string{
		"2024-01-15T14:30:00Z":        "2024-01-15 14:30:00+00",
		"2024-01-15T14:30:00.5Z":      "2024-01-15 14:30:00+00",
		"2024-01-15T14:30:00+02:00":   "2024-01-15 12:30:00+00",
		"2024-01-15T14:30:00.5-05:00": "2024-01-15 19:30:00+00", // used to panic
	}
	for in, want := range cases {
		got, err := pgbackrestTargetTime(in)
		if err != nil || got != want {
			t.Errorf("pgbackrestTargetTime(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := pgbackrestTargetTime("yesterday"); err == nil {
		t.Error("expected error for non-RFC3339 input")
	}
}

// A location whose check fails waits at the back of the queue while the next
// one runs; one failing every check fails after CheckTries; a backup runs once.
func TestBackupRound(t *testing.T) {
	checks := map[int]int{}
	var order []int
	check := func(_ context.Context, repo int) error {
		checks[repo]++
		order = append(order, repo)
		if repo == 3 || (repo == 1 && checks[1] == 1) {
			return errors.New("unreachable")
		}
		return nil
	}
	backups := map[int]int{}
	backup := func(_ context.Context, repo int) error {
		backups[repo]++
		if repo == 2 {
			return errors.New("disk full")
		}
		return nil
	}
	var warns, errs int
	report := func(e protocol.BackupEventPayload) {
		if e.Error {
			errs++
		} else {
			warns++
		}
	}
	p := protocol.PGBackRestBackupParams{Repos: []int{1, 2, 3}, CheckTries: 3}
	out, err := backupRound(context.Background(), p, check, backup, report)
	if err == nil {
		t.Fatal("want an error: two locations failed")
	}
	if want := []int{1, 2, 3, 1, 3, 3}; !slices.Equal(order, want) {
		t.Errorf("check order %v, want %v", order, want)
	}
	if backups[1] != 1 || backups[2] != 1 || backups[3] != 0 {
		t.Errorf("backups %v, want repo 1 and 2 once, 3 never", backups)
	}
	if warns != 3 || errs != 2 {
		t.Errorf("%d warnings, %d errors; want 3, 2", warns, errs)
	}
	if want := `[{"repo":2,"error":"disk full"},{"repo":1},{"repo":3,"error":"storage location can't be read (3 checks failed), not backed up: unreachable"}]`; out != want {
		t.Errorf("output\n%s\nwant\n%s", out, want)
	}
}
