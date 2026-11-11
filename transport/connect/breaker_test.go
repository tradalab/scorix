package connect

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type callee struct {
	hits  atomic.Int32
	reply atomic.Int32
}

func (c *callee) dial(t *testing.T) (*Conn, *time.Time) {
	t.Helper()
	h := connect.NewUnaryHandler(echo, func(_ context.Context, r *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
		c.hits.Add(1)
		if code := connect.Code(c.reply.Load()); code != 0 {
			return nil, connect.NewError(code, errors.New("as told"))
		}
		return connect.NewResponse(r.Msg), nil
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn := Dial(srv.URL, ConnOptions{})
	clock := time.Unix(1_000_000, 0)
	conn.now = func() time.Time { return clock }
	conn.roll = func() float64 { return 0 }
	return conn, &clock
}

func say(conn *Conn) error {
	_, err := Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("x"))
	return err
}

func TestACalleeThatKeepsRefusingOnBusinessGroundsIsNeverCutOff(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	for _, code := range []connect.Code{connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeAlreadyExists, connect.CodeFailedPrecondition, connect.CodeUnauthenticated} {
		c.reply.Store(int32(code))
		for range 10 {
			say(conn)
		}
	}
	c.reply.Store(0)
	if err := say(conn); err != nil || c.hits.Load() != 61 {
		t.Fatalf("after 60 refusals the call = %v with %d hits, want it to reach the callee", err, c.hits.Load())
	}
}

func TestACalleeThatKeepsFailingIsCutOffUntilTheWindowMovesOn(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnavailable, connect.CodeInternal, connect.CodeUnknown, connect.CodeDeadlineExceeded, connect.CodeDataLoss, connect.CodeUnimplemented} {
		var c callee
		conn, clock := c.dial(t)
		c.reply.Store(int32(code))
		for range 10 {
			say(conn)
		}
		hits := c.hits.Load()
		err := say(conn)
		if connect.CodeOf(err) != connect.CodeUnavailable || c.hits.Load() != hits {
			t.Fatalf("%s: after 10 failures the call = %v and reached the callee %d more times", code, err, c.hits.Load()-hits)
		}
		c.reply.Store(0)
		*clock = clock.Add(11 * time.Second)
		if err := say(conn); err != nil {
			t.Fatalf("%s: once the failures left the window the call = %v", code, err)
		}
	}
}

func TestAFewFailuresAmongSuccessesDoNotCutOff(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	for range 20 {
		say(conn)
	}
	c.reply.Store(int32(connect.CodeUnavailable))
	for range 5 {
		say(conn)
	}
	if hits := c.hits.Load(); hits != 25 {
		t.Fatalf("%d of 25 calls reached the callee", hits)
	}
}

func TestEachProcedureHasItsOwnBreaker(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	c.reply.Store(int32(connect.CodeUnavailable))
	for range 10 {
		say(conn)
	}
	c.reply.Store(0)
	_, err := Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, "/test.Echo/Other", wrapperspb.String("x"))
	if connect.CodeOf(err) == connect.CodeUnavailable {
		t.Fatalf("a failing procedure cut off another one: %v", err)
	}
}

func TestACallTheCallerCancelledIsNotTheCalleesFailure(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 10 {
		Call[wrapperspb.StringValue, wrapperspb.StringValue](ctx, conn, echo, wrapperspb.String("x"))
	}
	if err := say(conn); err != nil {
		t.Fatalf("after 10 calls the caller cancelled the next call = %v", err)
	}
}

func TestAFailingCalleeStillGetsTheShareOfCallsThatProbesItsRecovery(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	conn.roll = func() float64 { return 0.99 }
	c.reply.Store(int32(connect.CodeUnavailable))
	for range 10 {
		say(conn)
	}
	c.reply.Store(0)
	if err := say(conn); err != nil {
		t.Fatalf("a call whose roll beat the reject share = %v", err)
	}
}

func TestACallThatRanOutOfTheCallersOwnDeadlineIsNotTheCalleesFailure(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	kinds := map[string]func() error{
		"unary": func() error {
			_, err := Call[msg, msg](ctx, conn, echo, wrapperspb.String("x"))
			return err
		},
		"server stream": func() error {
			return Stream(ctx, conn, echo, wrapperspb.String("x"), func(*msg) error { return nil })
		},
		"client stream": func() error {
			_, err := CallClientStream[msg, msg](ctx, conn, echo, feed("x"))
			return err
		},
		"bidi stream": func() error {
			return CallBidiStream(ctx, conn, echo, feed("x"), func(*msg) error { return nil })
		},
	}
	for name, run := range kinds {
		for range 10 {
			if err := run(); connect.CodeOf(err) != connect.CodeDeadlineExceeded {
				t.Fatalf("%s past its deadline = %v", name, err)
			}
		}
	}
	if err := say(conn); err != nil {
		t.Fatalf("after 40 calls past the caller's deadline the next call = %v", err)
	}
}

func TestAStreamCutByTheCallersOwnDeadlineIsNotTheCalleesFailure(t *testing.T) {
	h := connect.NewServerStreamHandler(echo, func(ctx context.Context, _ *connect.Request[msg], st *connect.ServerStream[msg]) error {
		st.Send(wrapperspb.String("a"))
		<-ctx.Done()
		return ctx.Err()
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn := Dial(srv.URL, ConnOptions{})
	conn.now = func() time.Time { return time.Unix(1_000_000, 0) }
	conn.roll = func() float64 { return 0 }
	for range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := Stream(ctx, conn, echo, wrapperspb.String("x"), func(*msg) error { return nil })
		cancel()
		if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
			t.Fatalf("a stream past its deadline = %v", err)
		}
	}
}

func TestTheCalleesOwnDeadlineCountsWhileTheCallerStillHasTime(t *testing.T) {
	var c callee
	conn, _ := c.dial(t)
	c.reply.Store(int32(connect.CodeDeadlineExceeded))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for range 10 {
		Call[msg, msg](ctx, conn, echo, wrapperspb.String("x"))
	}
	if _, err := Call[msg, msg](ctx, conn, echo, wrapperspb.String("x")); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("after 10 of the callee's own timeouts the call = %v", err)
	}
}
