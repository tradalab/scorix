package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/transport/internal/testpb"
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

func TestTheObserverSeesEveryToolCallWithItsOutcome(t *testing.T) {
	w := &watcher{}
	ran := false
	srv, _ := serve(t, Options{Observer: w}, func(ctx context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		ran = watched(ctx)
		if in.GetId() == "gone" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("gone"))
		}
		return in, nil
	})
	post(t, srv, "ana", call("things_get", `{"id":"a"}`))
	if got := w.only(); got.transport != "mcp" || got.procedure != "/acme.Things/Get" || got.ends != 1 || got.err != nil || !ran {
		t.Fatalf("ok call observed %+v, ran=%v", got, ran)
	}
	for name, c := range map[string]struct {
		token, args string
		want        connect.Code
	}{"refused": {"", `{"id":"a"}`, connect.CodeUnauthenticated}, "failed": {"ana", `{"id":"gone"}`, connect.CodeNotFound}} {
		w.calls = nil
		post(t, srv, c.token, call("things_get", c.args))
		if got := w.only(); got.ends != 1 || connect.CodeOf(got.err) != c.want {
			t.Errorf("%s: observed %+v", name, got)
		}
	}
}

func TestAPanickingToolIsObservedAsInternalAndStillPanics(t *testing.T) {
	w := &watcher{}
	srv, _ := serve(t, Options{Observer: w}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) { panic("boom") })
	p := func() (p any) {
		defer func() { p = recover() }()
		req := httptest.NewRequest("POST", "/", strings.NewReader(call("things_get", `{}`)))
		req.Header.Set("Authorization", "Bearer ana")
		srv.Config.Handler.ServeHTTP(httptest.NewRecorder(), req)
		return nil
	}()
	if got := w.only(); p != "boom" || got.ends != 1 || connect.CodeOf(got.err) != connect.CodeInternal {
		t.Fatalf("panic %v, observed %+v", p, got)
	}
}
