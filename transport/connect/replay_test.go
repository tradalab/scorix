package connect

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
)

type replaying struct {
	runs  atomic.Int32
	reply func(n int32, in string) (*wrapperspb.StringValue, error)
	base  string
}

func newReplaying(t *testing.T, rule kernel.Rule) *replaying {
	t.Helper()
	r := &replaying{reply: func(n int32, in string) (*wrapperspb.StringValue, error) {
		return wrapperspb.String(in + "#" + string(rune('0'+n))), nil
	}}
	m := mux(t, Options{Auth: trustAll, Replay: memoryReplay(t)})
	Unary(m, echo, rule, func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return r.reply(r.runs.Add(1), in.GetValue())
	})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	r.base = srv.URL
	return r
}

func memoryReplay(t *testing.T) kernel.ReplayStore {
	t.Helper()
	s, err := kernel.NewMemoryReplay(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (r *replaying) call(who, key, text string) (string, error) {
	conn := Dial(r.base, ConnOptions{Header: func(_ context.Context, h http.Header) {
		h.Set("X-Who", who)
		if key != "" {
			h.Set("Idempotency-Key", key)
		}
	}})
	res, err := Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String(text))
	return res.GetValue(), err
}

func TestARetryWithTheSameKeyGetsTheFirstReplyWithoutASecondRun(t *testing.T) {
	r := newReplaying(t, kernel.Rule{Replay: true})
	a, err1 := r.call("u", "k1", "x")
	b, err2 := r.call("u", "k1", "x")
	if err1 != nil || err2 != nil || a != "x#1" || b != "x#1" || r.runs.Load() != 1 {
		t.Fatalf("first %q %v, retry %q %v, %d runs", a, err1, b, err2, r.runs.Load())
	}
	if c, _ := r.call("u", "", "x"); c != "x#2" {
		t.Errorf("a call with no key = %q, want a fresh run", c)
	}
	if c, _ := r.call("v", "k1", "x"); c != "x#3" {
		t.Errorf("another principal's same key = %q, want its own run", c)
	}
}

func TestAKeyReusedForAnotherRequestIsRefusedWithoutARun(t *testing.T) {
	r := newReplaying(t, kernel.Rule{Replay: true})
	r.call("u", "k1", "x")
	if _, err := r.call("u", "k1", "y"); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "different request") || r.runs.Load() != 1 {
		t.Fatalf("reused key = %v after %d runs", err, r.runs.Load())
	}
	if _, err := r.call("u", strings.Repeat("k", 256), "x"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a 256 byte key = %v", err)
	}
}

func TestABusinessRefusalIsReplayedButAFailureLetsTheRetryRun(t *testing.T) {
	r := newReplaying(t, kernel.Rule{Replay: true})
	r.reply = func(n int32, in string) (*wrapperspb.StringValue, error) {
		if in == "missing" {
			return nil, fault.New(fault.CodeNotFound, "no such link")
		}
		if n == 2 {
			return nil, fault.New(fault.CodeUnavailable, "database down")
		}
		return wrapperspb.String("ok"), nil
	}
	r.call("u", "k1", "missing")
	if _, err := r.call("u", "k1", "missing"); connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "no such link") || r.runs.Load() != 1 {
		t.Fatalf("replayed refusal = %v after %d runs", err, r.runs.Load())
	}
	if _, err := r.call("u", "k2", "x"); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("second run = %v", err)
	}
	if got, err := r.call("u", "k2", "x"); err != nil || got != "ok" || r.runs.Load() != 3 {
		t.Fatalf("retry after a failure = %q %v after %d runs", got, err, r.runs.Load())
	}
}

func TestARetryWhileTheFirstStillRunsIsAborted(t *testing.T) {
	r := newReplaying(t, kernel.Rule{Replay: true})
	entered, release := make(chan struct{}), make(chan struct{})
	r.reply = func(int32, string) (*wrapperspb.StringValue, error) {
		close(entered)
		<-release
		return wrapperspb.String("done"), nil
	}
	first := make(chan error, 1)
	go func() { _, err := r.call("u", "k1", "x"); first <- err }()
	<-entered
	_, err := r.call("u", "k1", "x")
	close(release)
	if connect.CodeOf(err) != connect.CodeAborted || <-first != nil {
		t.Fatalf("concurrent retry = %v", err)
	}
}

func TestAKeyOnAProcedureWithoutReplayIsIgnored(t *testing.T) {
	r := newReplaying(t, kernel.Rule{})
	r.call("u", "k1", "x")
	if got, _ := r.call("u", "k1", "x"); got != "x#2" {
		t.Fatalf("got %q, want a second run", got)
	}
}

func TestReplayWithoutAStoreIsRefusedAtRegistration(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "replays an Idempotency-Key and the Mux has no Replay store") {
			t.Fatalf("panic = %v", r)
		}
	}()
	Unary(mux(t, Options{Auth: trustAll}), echo, kernel.Rule{Replay: true}, func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, errors.New("unreachable")
	})
}

type legacyRefusal struct{}

func (legacyRefusal) Error() string { return "legacy: no such row" }

func TestAnErrorTheWireOptionNamesIsKeptUnderThatName(t *testing.T) {
	runs := 0
	m := mux(t, Options{Auth: trustAll, Replay: memoryReplay(t), Wire: func(err error) *connect.Error {
		if _, ok := err.(legacyRefusal); ok {
			return connect.NewError(connect.CodeNotFound, err)
		}
		return nil
	}})
	Unary(m, echo, kernel.Rule{Replay: true}, func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		runs++
		return nil, legacyRefusal{}
	})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	r := &replaying{base: srv.URL}
	r.call("u", "k", "x")
	if _, err := r.call("u", "k", "x"); connect.CodeOf(err) != connect.CodeNotFound || runs != 1 {
		t.Fatalf("retry = %v after %d runs, want the kept not_found", err, runs)
	}
}

func TestKeysOfDifferentCallersNeverMeetHoweverTheirPartsAreSpelled(t *testing.T) {
	store := memoryReplay(t)
	runs := 0
	fn := func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		runs++
		return in, nil
	}
	as := func(id string) context.Context {
		return kernel.WithPrincipal(context.Background(), kernel.Principal{Kind: kernel.User, ID: id})
	}
	in := wrapperspb.String("x")
	if _, err := Replay(as("u\x00"+echo+"\x00k"), store, echo, "k2", in, fn); err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(as("u"), store, echo, "k\x00"+echo+"\x00k2", in, fn); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("%d runs; the second caller got the first caller's reply", runs)
	}
}

func TestAPanickingHandlerFreesItsKeySoTheRetryRuns(t *testing.T) {
	store := memoryReplay(t)
	runs := 0
	fn := func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		runs++
		if runs == 1 {
			panic("boom")
		}
		return wrapperspb.String("ok"), nil
	}
	ctx := kernel.WithPrincipal(context.Background(), kernel.Principal{Kind: kernel.User, ID: "u"})
	func() {
		defer func() { recover() }()
		Replay(ctx, store, echo, "k", wrapperspb.String("x"), fn)
	}()
	if res, err := Replay(ctx, store, echo, "k", wrapperspb.String("x"), fn); err != nil || res.GetValue() != "ok" || runs != 2 {
		t.Fatalf("the retry after a panic = %v, %v after %d runs", res, err, runs)
	}
}

type failingStore struct{ kernel.ReplayStore }

func (failingStore) Keep(context.Context, string, string, kernel.Replayed) error {
	return errors.New("keep lost")
}
func (failingStore) Drop(context.Context, string, string) error { return errors.New("drop lost") }

func TestAStoreThatCannotKeepOrDropIsLogged(t *testing.T) {
	var logged strings.Builder
	ctx := observe.WithLogger(kernel.WithPrincipal(context.Background(), kernel.Principal{Kind: kernel.User, ID: "u"}),
		slog.New(slog.NewTextHandler(&logged, nil)))
	store := failingStore{memoryReplay(t)}
	Replay(ctx, store, echo, "kept", wrapperspb.String("x"), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("ok"), nil
	})
	Replay(ctx, store, echo, "dropped", wrapperspb.String("x"), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	})
	for _, want := range []string{"keep lost", "drop lost"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log %q lacks %q", logged.String(), want)
		}
	}
}
