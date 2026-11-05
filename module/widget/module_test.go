package widget

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestSetTimeline(t *testing.T) {
	m := New()
	for name, bad := range map[string]Timeline{
		"no kind": {Entries: []Entry{{At: 1, Title: "15"}}},
		"entries": {Kind: "today", Entries: make([]Entry, maxEntries+1)},
		"rows":    {Kind: "today", Entries: []Entry{{Rows: make([]Row, maxRows+1)}}},
		"cells":   {Kind: "month", Fallback: Entry{Cells: make([]Cell, maxCells+1)}},
	} {
		if _, err := m.SetTimeline(context.Background(), bad); fault.CodeOf(err) != "invalid_request" {
			t.Errorf("%s: err = %v, want invalid_request", name, err)
		}
	}
	if _, err := m.SetTimeline(context.Background(), Timeline{Kind: "today", Entries: []Entry{{At: 1, Title: "15"}}}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("SetTimeline err = %v, want code unavailable", err)
	}
}

func TestRequestPin(t *testing.T) {
	m := New()
	if _, err := m.RequestPin(context.Background(), Pin{}); fault.CodeOf(err) != "invalid_request" {
		t.Errorf("no kind: err = %v, want invalid_request", err)
	}
	if _, err := m.RequestPin(context.Background(), Pin{Kind: "today"}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("RequestPin err = %v, want code unavailable", err)
	}
}
