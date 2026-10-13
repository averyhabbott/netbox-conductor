// Package cron parses and matches standard 5-field cron schedules. Validate
// and Matches share one parser, so a schedule that validates is one that runs.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Standard 5-field cron: minute hour day month weekday. Each field takes
// * (any), N, N-M, a /N step after * or a range or a start, and comma lists.
// Weekday 0 and 7 are both Sunday. All fields must match (day and weekday are
// not OR-ed as in Vixie cron). Names like MON or JAN aren't supported.

// cronSpec is a parsed expression: per field, one bit per allowed value.
type cronSpec [5]uint64

var cronFields = [5]struct {
	name   string
	lo, hi int
}{{"minute", 0, 59}, {"hour", 0, 23}, {"day", 1, 31}, {"month", 1, 12}, {"weekday", 0, 7}}

// Validate explains why expr isn't a valid cron expression, or returns nil.
func Validate(expr string) error {
	_, err := parseCron(expr)
	return err
}

func parseCron(expr string) (cronSpec, error) {
	var s cronSpec
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return s, errors.New("needs 5 fields: minute hour day month weekday")
	}
	for i, f := range fields {
		for _, part := range strings.Split(f, ",") {
			bits, err := cronPart(part, cronFields[i].lo, cronFields[i].hi)
			if err != nil {
				return s, fmt.Errorf("%s field %q: %w", cronFields[i].name, f, err)
			}
			s[i] |= bits
		}
	}
	if s[4]&(1<<7) != 0 {
		s[4] |= 1 // 7 = Sunday
	}
	return s, nil
}

func cronPart(part string, lo, hi int) (uint64, error) {
	rng, stepStr, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("step %q must be a whole number above 0", stepStr)
		}
		step = n
	}
	from, to := lo, hi
	if rng != "*" {
		a, b, isRange := strings.Cut(rng, "-")
		var err error
		if from, err = cronNum(a, lo, hi); err != nil {
			return 0, err
		}
		switch {
		case isRange:
			if to, err = cronNum(b, lo, hi); err != nil {
				return 0, err
			}
			if from > to {
				return 0, fmt.Errorf("range %q runs backwards", rng)
			}
		case !hasStep:
			to = from
		}
	}
	var bits uint64
	for v := from; v <= to; v += step {
		bits |= 1 << v
	}
	return bits, nil
}

func cronNum(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q is not a number", s)
	case n < lo || n > hi:
		return 0, fmt.Errorf("%d is outside %d–%d", n, lo, hi)
	}
	return n, nil
}

func (s cronSpec) matches(t time.Time) bool {
	vals := [5]int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	for i, v := range vals {
		if s[i]&(1<<v) == 0 {
			return false
		}
	}
	return true
}

// Matches reports whether t falls on expr; an invalid expr never matches.
func Matches(expr string, t time.Time) bool {
	s, err := parseCron(expr)
	return err == nil && s.matches(t)
}
