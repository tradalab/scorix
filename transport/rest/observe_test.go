package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestTheObserverSeesEveryRouteCallWithItsOutcome(t *testing.T) {
	w := &watcher{}
	ran := false
	srv := serve(t, Options{Observer: w}, Route{Method: "GET", Path: "/x/{id}", Procedure: "/acme.X/Get", Anonymous: true}, func(ctx context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		ran = watched(ctx)
		return in, nil
	})
	do(t, srv, "GET", "/x/a?page=2", "", http.Header{"X-Trace": {"t1"}})
	if got := w.only(); got.transport != "rest" || got.procedure != "/acme.X/Get" || got.header != "t1" || !got.anonymous || got.ends != 1 || got.err != nil || !ran {
		t.Fatalf("ok call observed %+v, ran=%v", got, ran)
	}
	w = &watcher{}
	srv = serve(t, Options{Observer: w}, Route{Method: "GET", Path: "/x/{id}", Procedure: "/acme.X/Get"}, echo)
	do(t, srv, "GET", "/x/a?page=nope", "", nil)
	if got := w.only(); got.ends != 1 || connect.CodeOf(got.err) != connect.CodeInvalidArgument {
		t.Fatalf("a call that does not bind observed %+v", got)
	}
}

func TestAPanickingRouteIsObservedAsInternalAndStillPanics(t *testing.T) {
	w := &watcher{}
	srv := serve(t, Options{Observer: w}, Route{Method: "GET", Path: "/x", Procedure: "/acme.X/Get"}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) { panic("boom") })
	p := func() (p any) {
		defer func() { p = recover() }()
		srv.Config.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
		return nil
	}()
	if got := w.only(); p != "boom" || got.ends != 1 || connect.CodeOf(got.err) != connect.CodeInternal {
		t.Fatalf("panic %v, observed %+v", p, got)
	}
}
