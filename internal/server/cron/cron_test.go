package cron

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	// Sunday 2026-10-11 01:30 UTC
	sun := time.Date(2026, 10, 11, 1, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		expr string
		want bool
	}{
		{"* * * * *", true},
		{"*/30 * * * *", true},
		{"*/7 * * * *", false},
		{"30 1 * * 0", true},
		{"30 1 * * 7", true},
		{"30 1 * * 1-6", false},
		{"0,15,30,45 0-3 * * *", true},
		{"10/20 * * * *", true}, // 10, 30, 50
		{"0-20/10 * * * *", false},
		{"30 1 11 10 *", true},
		{"30 1 12 10 *", false},
	} {
		if err := Validate(c.expr); err != nil {
			t.Errorf("%q rejected: %v", c.expr, err)
		}
		if got := Matches(c.expr, sun); got != c.want {
			t.Errorf("%q matches = %v, want %v", c.expr, got, c.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "1,,2 * * * *", "MON * * * *", "a * * * *", "-1 * * * *"} {
		if Validate(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
