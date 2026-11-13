package connect

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

type deadlineSeen struct{ left time.Duration }

func (d *deadlineSeen) Do(req *http.Request) (*http.Response, error) {
	d.left = -1
	if at, ok := req.Context().Deadline(); ok {
		d.left = time.Until(at)
	}
	return nil, errors.New("recorded")
}

func TestAUnaryCallEndsAtTheEarliestOfTheCallerDeadlineAndItsMethodOrConnBound(t *testing.T) {
	call := func(ctx context.Context, timeout time.Duration) time.Duration {
		seen := &deadlineSeen{}
		Call[wrapperspb.StringValue, wrapperspb.StringValue](ctx, Dial("http://callee", ConnOptions{HTTPClient: seen, Timeout: timeout}), echo, wrapperspb.String("x"))
		return seen.left
	}
	within := func(t *testing.T, name string, left, lo, hi time.Duration) {
		t.Helper()
		if left <= lo || left > hi {
			t.Errorf("%s: the call had %v left, want between %v and %v", name, left, lo, hi)
		}
	}
	within(t, "no caller deadline", call(context.Background(), 2*time.Second), time.Second, 2*time.Second)
	long, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	within(t, "a caller deadline longer than the conn bound", call(long, 2*time.Second), time.Second, 2*time.Second)
	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	within(t, "a caller deadline shorter than the conn bound", call(short, 2*time.Second), 0, 500*time.Millisecond)
	method, cancelMethod := MethodTimeout(context.Background(), 150*time.Second)
	defer cancelMethod()
	within(t, "a method bound longer than the conn bound", call(method, 2*time.Second), 149*time.Second, 150*time.Second)
	methodInShort, cancelBoth := MethodTimeout(short, 150*time.Second)
	defer cancelBoth()
	within(t, "a method bound inside a shorter caller deadline", call(methodInShort, 2*time.Second), 0, 500*time.Millisecond)
	if left := call(context.Background(), 0); left != -1 {
		t.Errorf("no bound and no caller deadline: %v left, want none", left)
	}
}
