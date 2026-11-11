package worker

import (
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04 Mon", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextIsTheFirstMatchingMinuteAfter(t *testing.T) {
	for _, c := range []struct{ spec, from, want string }{
		{"*/15 * * * *", "2026-11-11 10:07 Wed", "2026-11-11 10:15 Wed"},
		{"*/15 * * * *", "2026-11-11 10:15 Wed", "2026-11-11 10:30 Wed"},
		{"0 9 * * 1-5", "2026-11-13 10:00 Fri", "2026-11-16 09:00 Mon"},
		{"30 23 * * *", "2026-12-31 23:45 Thu", "2027-01-01 23:30 Fri"},
		{"0 0 1 * 1", "2026-11-11 10:00 Wed", "2026-11-16 00:00 Mon"},
		{"0 0 13 * 5", "2026-11-11 10:00 Wed", "2026-11-13 00:00 Fri"},
		{"0 0 * * 7", "2026-11-11 10:00 Wed", "2026-11-15 00:00 Sun"},
		{"0 0 * * 5-7", "2026-11-14 01:00 Sat", "2026-11-15 00:00 Sun"},
		{"10-20/5 8 * * *", "2026-11-11 08:12 Wed", "2026-11-11 08:15 Wed"},
		{"5/20 * * * *", "2026-11-11 08:26 Wed", "2026-11-11 08:45 Wed"},
		{"0 12 29 2 *", "2026-11-11 10:00 Wed", "2028-02-29 12:00 Tue"},
		{"@daily", "2026-11-11 10:00 Wed", "2026-11-12 00:00 Thu"},
		{"@hourly", "2026-11-11 10:00 Wed", "2026-11-11 11:00 Wed"},
		{"@weekly", "2026-11-11 10:00 Wed", "2026-11-15 00:00 Sun"},
		{"@monthly", "2026-11-11 10:00 Wed", "2026-12-01 00:00 Tue"},
		{"0,30 * * 1,6 *", "2026-11-11 10:00 Wed", "2027-01-01 00:00 Fri"},
	} {
		s, err := ParseSchedule(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := s.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.spec, c.from, got.Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
	never, _ := ParseSchedule("0 0 31 2 *")
	if got := never.Next(at("2026-11-11 10:00 Wed")); !got.IsZero() {
		t.Errorf("a 31st of February = %v", got)
	}
}

func TestASchedulesBadFieldsAreNamed(t *testing.T) {
	for spec, want := range map[string]string{
		"* * * *":     "has 4 fields",
		"60 * * * *":  "minute",
		"* 24 * * *":  "hour",
		"* * 0 * *":   "day of month",
		"* * * 13 *":  "month",
		"* * * * 8":   "day of week",
		"*/0 * * * *": "step",
		"5-1 * * * *": "outside",
		"x * * * *":   "not a number",
		"1-x * * * *": "not a number",
	} {
		if _, err := ParseSchedule(spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", spec, err, want)
		}
	}
	s, _ := ParseSchedule(" 0 0 * * * ")
	if s.String() != " 0 0 * * * " {
		t.Errorf("String = %q", s.String())
	}
}

func inZone(t *testing.T, name, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no zone %s: %v", name, err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func nextWithin(t *testing.T, s Schedule, from time.Time) time.Time {
	t.Helper()
	got := make(chan time.Time, 1)
	go func() { got <- s.Next(from) }()
	select {
	case v := <-got:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s after %v never returned", s, from)
		return time.Time{}
	}
}

func TestNextSkipsTheHourASpringForwardRemoves(t *testing.T) {
	for _, c := range []struct{ spec, zone, from, want string }{
		{"0 2 * * *", "America/New_York", "2026-03-08 01:00", "2026-03-09 02:00"},
		{"30 * * * *", "America/New_York", "2026-03-08 01:45", "2026-03-08 03:30"},
		{"0 3 * * *", "America/New_York", "2026-03-08 00:10", "2026-03-08 03:00"},
		{"0 12 * * *", "America/Santiago", "2026-09-05 23:30", "2026-09-06 12:00"},
		{"0 12 * * 0", "America/Santiago", "2026-09-05 22:00", "2026-09-06 12:00"},
		{"0 0 1 10 *", "America/Santiago", "2026-09-05 22:00", "2026-10-01 00:00"},
	} {
		s, _ := ParseSchedule(c.spec)
		if got := nextWithin(t, s, inZone(t, c.zone, c.from)); !got.Equal(inZone(t, c.zone, c.want)) {
			t.Errorf("%s in %s after %s = %v, want %s", c.spec, c.zone, c.from, got, c.want)
		}
	}
}

func TestAWallTimeAFallBackRepeatsRunsOnceUnlessTheHourFieldIsAStar(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	count := func(spec string) int {
		s, _ := ParseSchedule(spec)
		n := 0
		for v := nextWithin(t, s, from); v.Before(from.Add(4 * time.Hour)); v = nextWithin(t, s, v) {
			n++
		}
		return n
	}
	if n := count("30 1 * * *"); n != 1 {
		t.Errorf("30 1 * * * ran %d times on the fall-back day, want once", n)
	}
	if n := count("30 * * * *"); n != 4 {
		t.Errorf("30 * * * * ran %d times in the four hours, want each of them", n)
	}
}

func TestAStarPrefixedDayFieldNarrowsTheOtherAsInVixieCron(t *testing.T) {
	for _, c := range []struct{ spec, from, want string }{
		{"0 0 */2 * 1", "2026-11-11 10:00 Wed", "2026-11-23 00:00 Mon"},
		{"0 0 1 * */3", "2026-11-11 10:00 Wed", "2027-05-01 00:00 Sat"},
		{"0 0 */10 * *", "2026-11-11 10:00 Wed", "2026-11-21 00:00 Sat"},
		{"0 0 * * */3", "2026-11-11 10:00 Wed", "2026-11-14 00:00 Sat"},
	} {
		s, _ := ParseSchedule(c.spec)
		if got := s.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.spec, c.from, got.Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
}
