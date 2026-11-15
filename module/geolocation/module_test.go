package geolocation

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestUnavailableWithoutHost(t *testing.T) {
	if _, err := New().Current(context.Background(), Request{}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("Current err = %v, want code unavailable", err)
	}
}

func TestRejectsTimeoutsOutOfRangeBeforeAskingTheHost(t *testing.T) {
	for _, r := range []Request{{TimeoutMs: -1}, {MaxAgeMs: -1}, {TimeoutMs: maxTimeoutMs + 1}} {
		if _, err := New().Current(context.Background(), r); fault.CodeOf(err) != "invalid_request" {
			t.Fatalf("Current(%+v) err = %v, want code invalid_request", r, err)
		}
	}
}
