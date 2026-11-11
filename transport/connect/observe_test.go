package connect

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
)

type seen struct {
	transport, procedure, header string
	anonymous                    bool
	err                          error
	ends                         int
}

type watcher struct {
	mu    sync.Mutex
	calls []*seen
}

type watchKey struct{}

func (w *watcher) Start(ctx context.Context, transport string, c kernel.Call) (context.Context, func(error)) {
	s := &seen{transport: transport, procedure: c.Procedure, header: c.Header.Get("X-Trace"), anonymous: c.Anonymous}
	w.mu.Lock()
	w.calls = append(w.calls, s)
	w.mu.Unlock()
	return context.WithValue(ctx, watchKey{}, "watched"), func(err error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		s.err = err
		s.ends++
	}
}

func (w *watcher) only() seen {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.calls) != 1 {
		return seen{ends: -len(w.calls)}
	}
	return *w.calls[0]
}

func watched(ctx context.Context) bool { return ctx.Value(watchKey{}) == "watched" }

func TestTheObserverSeesEveryCallWithItsOutcome(t *testing.T) {
	for name, c := range map[string]struct {
		auth kernel.Authenticator
		text string
		want connect.Code
	}{
		"ok":    {trustAll, "hi", 0},
		"fault": {trustAll, "missing", connect.CodeNotFound},
		"refused": {kernel.AuthenticatorFunc(func(context.Context, kernel.Call) (context.Context, error) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no"))
		}), "hi", connect.CodeUnauthenticated},
	} {
		w := &watcher{}
		ran := false
		cl := serve(t, Options{Auth: c.auth, Observer: w}, func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			ran = watched(ctx)
			if in.GetValue() == "missing" {
				return nil, fault.New(fault.CodeNotFound, "gone")
			}
			return in, nil
		})
		req := connect.NewRequest(wrapperspb.String(c.text))
		req.Header().Set("X-Trace", "t1")
		cl.CallUnary(context.Background(), req)
		got := w.only()
		if got.transport != "connect" || got.procedure != echo || got.header != "t1" || got.ends != 1 || codeOrZero(got.err) != c.want {
			t.Errorf("%s: observed %+v", name, got)
		}
		if c.want != connect.CodeUnauthenticated && !ran {
			t.Errorf("%s: the handler did not run under the observer's context", name)
		}
	}
}

func codeOrZero(err error) connect.Code {
	if err == nil {
		return 0
	}
	return connect.CodeOf(err)
}

func TestEveryStreamKindIsObserved(t *testing.T) {
	w := &watcher{}
	m := mux(t, Options{Auth: trustAll, Observer: w})
	ServerStream(m, count, kernel.Rule{}, letters)
	ClientStream(m, join, kernel.Rule{}, joining)
	BidiStream(m, chat, kernel.Rule{}, upper)
	srv := tlsServer(t, m)
	conn := dialed(srv, false)
	Stream(context.Background(), conn, count, wrapperspb.String("ab"), func(*wrapperspb.StringValue) error { return nil })
	CallClientStream[msg, msg](context.Background(), conn, join, feed("a", "boom"))
	CallBidiStream(context.Background(), conn, chat, feed("a"), func(*msg) error { return nil })
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.calls) != 3 {
		t.Fatalf("observed %d calls, want 3", len(w.calls))
	}
	for i, want := range []struct {
		procedure string
		code      connect.Code
	}{{count, 0}, {join, connect.CodeInvalidArgument}, {chat, 0}} {
		if c := w.calls[i]; c.procedure != want.procedure || c.ends != 1 || codeOrZero(c.err) != want.code {
			t.Errorf("call %d = %+v, want %s ending %v", i, *c, want.procedure, want.code)
		}
	}
}

func TestAPanickingHandlerIsObservedAsInternalAndStillPanics(t *testing.T) {
	w := &watcher{}
	m := mux(t, Options{Auth: trustAll, Observer: w})
	Unary(m, echo, kernel.Rule{}, func(context.Context, *msg) (*msg, error) { panic("unary") })
	ServerStream(m, count, kernel.Rule{}, func(context.Context, *msg, func(*msg) error) error { panic("server") })
	ClientStream(m, join, kernel.Rule{}, func(context.Context, func() (*msg, error)) (*msg, error) { panic("client") })
	BidiStream(m, chat, kernel.Rule{}, func(context.Context, func() (*msg, error), func(*msg) error) error { panic("bidi") })
	var panics atomic.Int32
	h := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				panics.Add(1)
				panic(http.ErrAbortHandler)
			}
		}()
		m.ServeHTTP(rw, r)
	})
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	conn := dialed(srv, false)
	ctx := context.Background()
	Call[msg, msg](ctx, conn, echo, wrapperspb.String("a"))
	Stream(ctx, conn, count, wrapperspb.String("a"), func(*msg) error { return nil })
	CallClientStream[msg, msg](ctx, conn, join, feed("a"))
	CallBidiStream(ctx, conn, chat, feed("a"), func(*msg) error { return nil })
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.calls) != 4 || panics.Load() != 4 {
		t.Fatalf("observed %d calls and %d panics, want 4 of each", len(w.calls), panics.Load())
	}
	for _, c := range w.calls {
		if c.ends != 1 || codeOrZero(c.err) != connect.CodeInternal {
			t.Errorf("%s ended %d times with %v, want once with internal", c.procedure, c.ends, c.err)
		}
	}
}
