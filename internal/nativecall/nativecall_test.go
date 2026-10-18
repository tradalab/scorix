package nativecall

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCallWithoutHost(t *testing.T) {
	Install(nil, nil)
	if _, err := Call(context.Background(), "p", "m", nil); !errors.Is(err, ErrNoHost) {
		t.Fatalf("err = %v, want ErrNoHost", err)
	}
}

func TestCallRoundTrip(t *testing.T) {
	Install(func(b []byte) {
		var req request
		_ = json.Unmarshal(b, &req)
		reply, _ := json.Marshal(Reply{ID: req.ID, OK: true, Data: json.RawMessage(`{"echo":` + string(req.Args) + `}`)})
		go Deliver(reply)
	}, func([]byte) {})
	t.Cleanup(func() { Install(nil, nil) })

	r, err := Call(context.Background(), "echo", "say", json.RawMessage(`"hi"`))
	if err != nil || !r.OK || string(r.Data) != `{"echo":"hi"}` {
		t.Fatalf("reply = %+v, %v", r, err)
	}
}

func TestCancelTellsHostAndDropsLateReply(t *testing.T) {
	var sent request
	canceled := make(chan uint64, 1)
	Install(func(b []byte) { _ = json.Unmarshal(b, &sent) }, func(b []byte) {
		var c struct{ ID uint64 }
		_ = json.Unmarshal(b, &c)
		canceled <- c.ID
	})
	t.Cleanup(func() { Install(nil, nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := Call(ctx, "biometric", "authenticate", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if id := <-canceled; id != sent.ID {
		t.Fatalf("canceled id %d, want %d", id, sent.ID)
	}
	late, _ := json.Marshal(Reply{ID: sent.ID, OK: true})
	Deliver(late) // must not block or panic
	if len(pending) != 0 {
		t.Fatalf("pending leaked: %v", pending)
	}
}
