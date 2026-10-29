package notification

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestScheduleValidation(t *testing.T) {
	m := New()
	for _, r := range []ScheduleRequest{
		{Items: make([]ScheduledNotify, maxScheduled+1)},
		{Items: []ScheduledNotify{{At: 1}}},
	} {
		if _, err := m.Schedule(context.Background(), r); fault.CodeOf(err) != "invalid_request" {
			t.Errorf("Schedule(%d items) err = %v, want invalid_request", len(r.Items), err)
		}
	}
	if _, err := m.Schedule(context.Background(), ScheduleRequest{Items: []ScheduledNotify{{At: 1, Title: "Rằm"}}}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("Schedule err = %v, want code unavailable", err)
	}
}
