package worker

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a five-field cron expression: minute hour day-of-month month day-of-week, each
// a *, a number, a range a-b, a list, or a step */n or a-b/n. Sunday is 0 or 7. As in Vixie
// cron, a time matches either day field when neither starts with *, and both otherwise.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	anyDom, anyDow, anyHour       bool
	text                          string
}

var fields = []struct {
	name     string
	min, max int
}{{"minute", 0, 59}, {"hour", 0, 23}, {"day of month", 1, 31}, {"month", 1, 12}, {"day of week", 0, 7}}

// ParseSchedule reads spec, or one of @hourly, @daily, @weekly, @monthly.
func ParseSchedule(spec string) (Schedule, error) {
	text := strings.TrimSpace(spec)
	switch text {
	case "@hourly":
		text = "0 * * * *"
	case "@daily":
		text = "0 0 * * *"
	case "@weekly":
		text = "0 0 * * 0"
	case "@monthly":
		text = "0 0 1 * *"
	}
	parts := strings.Fields(text)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("worker: schedule %q has %d fields, want minute hour day-of-month month day-of-week", spec, len(parts))
	}
	var bits [5]uint64
	for i, p := range parts {
		b, err := parseField(p, fields[i].min, fields[i].max)
		if err != nil {
			return Schedule{}, fmt.Errorf("worker: schedule %q %s: %w", spec, fields[i].name, err)
		}
		bits[i] = b
	}
	if bits[4]&(1<<7) != 0 {
		bits[4] |= 1
	}
	return Schedule{minute: bits[0], hour: bits[1], dom: bits[2], month: bits[3], dow: bits[4] &^ (1 << 7),
		anyDom: strings.HasPrefix(parts[2], "*"), anyDow: strings.HasPrefix(parts[4], "*"), anyHour: strings.HasPrefix(parts[1], "*"), text: spec}, nil
}

func parseField(f string, lo, hi int) (uint64, error) {
	var out uint64
	for _, item := range strings.Split(f, ",") {
		rng, stepText, stepped := strings.Cut(item, "/")
		step := 1
		if stepped {
			n, err := strconv.Atoi(stepText)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("step %q is not a positive number", stepText)
			}
			step = n
		}
		a, b := lo, hi
		if rng != "*" {
			from, to, isRange := strings.Cut(rng, "-")
			var err error
			if a, err = strconv.Atoi(from); err != nil {
				return 0, fmt.Errorf("%q is not a number", from)
			}
			b = a
			if isRange {
				if b, err = strconv.Atoi(to); err != nil {
					return 0, fmt.Errorf("%q is not a number", to)
				}
			} else if stepped {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", item, lo, hi)
		}
		for v := a; v <= b; v += step {
			out |= 1 << v
		}
	}
	return out, nil
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<t.Day()) != 0
	dow := s.dow&(1<<int(t.Weekday())) != 0
	if s.anyDom || s.anyDow {
		return dom && dow
	}
	return dom || dow
}

func (s Schedule) repeated(t time.Time) bool {
	if s.anyHour {
		return false
	}
	return t.Add(-time.Hour).Format(time.DateTime) == t.Format(time.DateTime)
}

func later(t, next time.Time) time.Time {
	if next.After(t) {
		return next
	}
	return t.Add(time.Minute)
}

// Next is the first minute after t that matches, in t's location; the zero time when none
// comes within five years, as with a 31st of February. A wall time a DST fall-back repeats
// matches once, unless the hour field starts with *; one a spring-forward skips never does.
func (s Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<int(t.Month())) == 0 || !s.dayMatches(t):
			t = later(t, time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location()))
		case s.hour&(1<<t.Hour()) == 0:
			t = t.Add(time.Duration(60-t.Minute()) * time.Minute)
		case s.minute&(1<<t.Minute()) == 0 || s.repeated(t):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

func (s Schedule) String() string { return s.text }
