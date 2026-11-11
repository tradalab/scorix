package connect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
)

const (
	join = "/test.Echo/Join"
	chat = "/test.Echo/Chat"
)

type msg = wrapperspb.StringValue

func tlsServer(t *testing.T, m *Mux) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(m)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func feed(items ...string) func() (*msg, error) {
	return func() (*msg, error) {
		if len(items) == 0 {
			return nil, io.EOF
		}
		v := items[0]
		items = items[1:]
		return wrapperspb.String(v), nil
	}
}

func joining(ctx context.Context, recv func() (*msg, error)) (*msg, error) {
	p, _ := kernel.PrincipalFrom(ctx)
	var parts []string
	for {
		in, err := recv()
		if errors.Is(err, io.EOF) {
			return wrapperspb.String(p.ID + ":" + strings.Join(parts, "")), nil
		}
		if err != nil {
			return nil, err
		}
		if in.GetValue() == "boom" {
			return nil, fault.New(fault.CodeInvalid, "boom is not a letter")
		}
		parts = append(parts, in.GetValue())
	}
}

func dialed(srv *httptest.Server, grpc bool) *Conn {
	return Dial(srv.URL, ConnOptions{GRPC: grpc, HTTPClient: srv.Client(), Header: func(_ context.Context, h http.Header) { h.Set("X-Who", "ana") }})
}

func TestAClientStreamDeliversEveryMessageAndOneReplyOverConnectAndGRPC(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	ClientStream(m, join, kernel.Rule{}, joining)
	srv := tlsServer(t, m)
	for _, grpc := range []bool{false, true} {
		got, err := CallClientStream[msg, msg](context.Background(), dialed(srv, grpc), join, feed("a", "b", "c"))
		if err != nil || got.GetValue() != "ana:abc" {
			t.Errorf("grpc=%v = %v, %v", grpc, got, err)
		}
	}
	if _, err := CallClientStream[msg, msg](context.Background(), dialed(srv, false), join, feed("a", "boom")); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a handler fault = %v", err)
	}
}

func TestAClientStreamPassesTheGatesAndIsAudited(t *testing.T) {
	refuse := kernel.AuthenticatorFunc(func(context.Context, kernel.Call) (context.Context, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("who are you"))
	})
	l := &ledger{}
	m := mux(t, Options{Auth: refuse, Audit: l.record})
	ClientStream(m, join, kernel.Rule{Audit: true}, joining)
	if _, err := CallClientStream[msg, msg](context.Background(), dialed(tlsServer(t, m), false), join, feed("a")); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated = %v", err)
	}
	if len(l.seen) != 1 || l.seen[0].Procedure != join || connect.CodeOf(l.seen[0].Err) != connect.CodeUnauthenticated {
		t.Fatalf("audit = %+v", l.seen)
	}
}

func TestACallerWhoseOwnSourceFailsAbortsTheClientStream(t *testing.T) {
	aborted := make(chan error, 1)
	m := mux(t, Options{Auth: trustAll})
	ClientStream(m, join, kernel.Rule{}, func(ctx context.Context, recv func() (*msg, error)) (*msg, error) {
		for {
			if _, err := recv(); err != nil {
				aborted <- err
				return nil, err
			}
		}
	})
	srv := tlsServer(t, m)
	mine := errors.New("disk gone")
	sent := 0
	_, err := CallClientStream[msg, msg](context.Background(), dialed(srv, false), join, func() (*msg, error) {
		if sent++; sent == 3 {
			return nil, mine
		}
		return wrapperspb.String("x"), nil
	})
	if !errors.Is(err, mine) {
		t.Fatalf("CallClientStream = %v, want the source's error", err)
	}
	select {
	case got := <-aborted:
		if errors.Is(got, io.EOF) {
			t.Fatal("the handler saw a clean end, so it would act on a partial upload")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never learned the call was aborted")
	}
}

func TestAnEndlessSourceStopsWhenTheHandlerRefusesEarly(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	ClientStream(m, join, kernel.Rule{}, func(context.Context, func() (*msg, error)) (*msg, error) {
		return nil, fault.New(fault.CodeDenied, "not today")
	})
	srv := tlsServer(t, m)
	done := make(chan error, 1)
	go func() {
		_, err := CallClientStream[msg, msg](context.Background(), dialed(srv, false), join, func() (*msg, error) {
			return wrapperspb.String(strings.Repeat("x", 1024)), nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("CallClientStream = %v, want the handler's refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the caller kept sending after the handler had answered")
	}
}

func upper(ctx context.Context, recv func() (*msg, error), send func(*msg) error) error {
	p, _ := kernel.PrincipalFrom(ctx)
	if err := send(wrapperspb.String("hello " + p.ID)); err != nil {
		return err
	}
	for {
		in, err := recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if in.GetValue() == "boom" {
			return fault.New(fault.CodeNotFound, "no boom here")
		}
		if err := send(wrapperspb.String(strings.ToUpper(in.GetValue()))); err != nil {
			return err
		}
	}
}

func TestABidiStreamTalksBothWaysOverConnectAndGRPC(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	BidiStream(m, chat, kernel.Rule{}, upper)
	srv := tlsServer(t, m)
	for _, grpc := range []bool{false, true} {
		greeted := make(chan struct{})
		items := []string{"a", "b"}
		var got []string
		err := CallBidiStream(context.Background(), dialed(srv, grpc), chat, func() (*msg, error) {
			<-greeted
			if len(items) == 0 {
				return nil, io.EOF
			}
			v := items[0]
			items = items[1:]
			return wrapperspb.String(v), nil
		}, func(r *msg) error {
			if r.GetValue() == "hello ana" {
				close(greeted)
			}
			got = append(got, r.GetValue())
			return nil
		})
		if err != nil || strings.Join(got, ",") != "hello ana,A,B" {
			t.Errorf("grpc=%v = %v, %v", grpc, got, err)
		}
	}
}

func TestABidiStreamEndsWithTheFirstErrorOfEitherSide(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	BidiStream(m, chat, kernel.Rule{}, upper)
	srv := tlsServer(t, m)
	none := func(*msg) error { return nil }
	if err := CallBidiStream(context.Background(), dialed(srv, false), chat, feed("a", "boom", "c"), none); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("the handler's fault = %v", err)
	}
	stop := errors.New("enough")
	if err := CallBidiStream(context.Background(), dialed(srv, false), chat, feed("a", "b"), func(*msg) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("the reader's error = %v", err)
	}
	mine := errors.New("source gone")
	n := 0
	err := CallBidiStream(context.Background(), dialed(srv, false), chat, func() (*msg, error) {
		if n++; n == 2 {
			return nil, mine
		}
		return wrapperspb.String("x"), nil
	}, none)
	if !errors.Is(err, mine) {
		t.Errorf("the source's error = %v", err)
	}
}
