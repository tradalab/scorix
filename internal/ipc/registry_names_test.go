package ipc

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func TestNamesListsCommandsSorted(t *testing.T) {
	r := NewRegistry()
	nop := func(context.Context, json.RawMessage, Stream) (any, error) { return nil, nil }
	for _, name := range []string{"zeta:run", "alpha:run", "mid:run"} {
		r.Command(name, nop)
	}
	r.Event("some:event", func(context.Context, json.RawMessage) {})

	got := r.Names()
	want := []string{"alpha:run", "mid:run", "zeta:run"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	// Events are not callable through Invoke, so listing them would advertise
	// something every caller gets refused for.
	if slices.Contains(got, "some:event") {
		t.Error("an event leaked into the callable list")
	}
}
