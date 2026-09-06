package logger

import (
	"strings"
	"testing"
)

func TestRingKeepsTheLastLinesInOrder(t *testing.T) {
	SetRingSize(3)
	t.Cleanup(func() { SetRingSize(defaultRing) })

	for _, s := range []string{"one", "two", "three", "four"} {
		logRing.add(s)
	}
	got := strings.Join(Tail(), ",")
	if got != "two,three,four" {
		t.Fatalf("tail = %q, want the last three oldest-first", got)
	}
}

func TestRingSizeZeroDisablesCapture(t *testing.T) {
	SetRingSize(0)
	t.Cleanup(func() { SetRingSize(defaultRing) })

	New(Config{Level: "info", Format: "console", Output: "stdout", File: "logs/x.log", MaxSize: 1, MaxAge: 1})
	Info("nothing should be kept")
	if n := len(Tail()); n != 0 {
		t.Fatalf("ring holds %d lines with capture off", n)
	}
}

// The tee property: the sink must see the line unchanged.
func TestRingDoesNotSwallowTheLine(t *testing.T) {
	SetRingSize(8)
	t.Cleanup(func() { SetRingSize(defaultRing) })

	var sink strings.Builder
	w := ringWriter{nopSyncer{&sink}}
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "hello\n" {
		t.Fatalf("sink got %q", sink.String())
	}
	if got := Tail(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("ring got %v", got)
	}
}

type nopSyncer struct{ *strings.Builder }

func (nopSyncer) Sync() error { return nil }
