package connect

import (
	"context"
	"errors"
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

const count = "/test.Echo/Count"

func streaming(t *testing.T, opt Options, rule kernel.Rule, fn func(context.Context, *wrapperspb.StringValue, func(*wrapperspb.StringValue) error) error) *httptest.Server {
	t.Helper()
	m := mux(t, opt)
	ServerStream(m, count, rule, fn)
	return tlsServer(t, m)
}

func letters(_ context.Context, in *wrapperspb.StringValue, send func(*wrapperspb.StringValue) error) error {
	for _, r := range in.GetValue() {
		if err := send(wrapperspb.String(string(r))); err != nil {
			return err
		}
	}
	return nil
}

func collect(t *testing.T, srv *httptest.Server, grpc bool, text string) (string, error) {
	t.Helper()
	conn := Dial(srv.URL, ConnOptions{GRPC: grpc, HTTPClient: srv.Client(), Header: func(_ context.Context, h http.Header) { h.Set("X-Who", "u") }})
	var got []string
	err := Stream(context.Background(), conn, count, wrapperspb.String(text), func(m *wrapperspb.StringValue) error {
		got = append(got, m.GetValue())
		return nil
	})
	return strings.Join(got, ","), err
}

func TestAServerStreamSendsEveryMessageOverConnectAndGRPC(t *testing.T) {
	srv := streaming(t, Options{Auth: trustAll}, kernel.Rule{}, letters)
	for _, grpc := range []bool{false, true} {
		if got, err := collect(t, srv, grpc, "abc"); err != nil || got != "a,b,c" {
			t.Errorf("grpc=%v = %q, %v", grpc, got, err)
		}
	}
}

func TestAStreamPassesTheSameGatesAsAUnaryCall(t *testing.T) {
	refuse := kernel.AuthenticatorFunc(func(context.Context, kernel.Call) (context.Context, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("who are you"))
	})
	if _, err := collect(t, streaming(t, Options{Auth: refuse}, kernel.Rule{}, letters), false, "abc"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("unauthenticated stream = %v", err)
	}
	deny := kernel.PolicyFunc(func(context.Context, kernel.Principal, string) error { return fault.New(fault.CodeDenied, "no") })
	if got, err := collect(t, streaming(t, Options{Auth: trustAll, Policy: deny}, kernel.Rule{Permission: "count"}, letters), false, "abc"); connect.CodeOf(err) != connect.CodePermissionDenied || got != "" {
		t.Errorf("denied stream = %q, %v", got, err)
	}
}

func TestAStreamIsBoundedByItsRuleTimeoutAndEndsWithTheHandlersError(t *testing.T) {
	slow := func(ctx context.Context, _ *wrapperspb.StringValue, send func(*wrapperspb.StringValue) error) error {
		send(wrapperspb.String("first"))
		<-ctx.Done()
		return ctx.Err()
	}
	got, err := collect(t, streaming(t, Options{Auth: trustAll, Timeout: time.Minute}, kernel.Rule{Timeout: 50 * time.Millisecond}, slow), true, "")
	if got != "first" || connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Errorf("slow stream = %q, %v", got, err)
	}
	broken := func(_ context.Context, _ *wrapperspb.StringValue, send func(*wrapperspb.StringValue) error) error {
		send(wrapperspb.String("one"))
		return fault.New(fault.CodeNotFound, "gone midway")
	}
	got, err = collect(t, streaming(t, Options{Auth: trustAll}, kernel.Rule{}, broken), false, "")
	if got != "one" || connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("broken stream = %q, %v", got, err)
	}
}

func TestACallerThatStopsReadingEndsTheStream(t *testing.T) {
	ended := make(chan error, 1)
	endless := func(ctx context.Context, _ *wrapperspb.StringValue, send func(*wrapperspb.StringValue) error) error {
		for {
			if err := send(wrapperspb.String("x")); err != nil {
				ended <- err
				return err
			}
		}
	}
	srv := streaming(t, Options{Auth: trustAll}, kernel.Rule{}, endless)
	conn := Dial(srv.URL, ConnOptions{HTTPClient: srv.Client()})
	stop := errors.New("enough")
	n := 0
	err := Stream(context.Background(), conn, count, wrapperspb.String(""), func(*wrapperspb.StringValue) error {
		if n++; n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 3 {
		t.Fatalf("Stream = %v after %d messages, want the callback's error after 3", err, n)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler kept sending after the caller stopped reading")
	}
}
