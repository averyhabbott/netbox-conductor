package executor

import "testing"

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
