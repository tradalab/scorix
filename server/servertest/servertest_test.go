package servertest_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/server"
	"github.com/tradalab/scorix/server/servertest"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
)

const whoami = "/test.Echo/WhoAmI"

func service(t *testing.T) *servertest.Service {
	t.Helper()
	m, err := scorixconnect.NewMux(scorixconnect.Options{Auth: servertest.Auth, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	scorixconnect.Unary(m, whoami, kernel.Rule{}, func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		p, _ := kernel.PrincipalFrom(ctx)
		return wrapperspb.String(string(p.Kind) + "/" + p.ID), nil
	})
	return servertest.Start(t, server.Options{}, func(s *server.Server) { s.Mount("/test.Echo/", m) })
}

func ask(conn *scorixconnect.Conn) (string, error) {
	res, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, whoami, wrapperspb.String(""))
	return res.GetValue(), err
}

func TestACallRunsAsThePrincipalItWasDialedAsOverConnectAndGRPC(t *testing.T) {
	svc := service(t)
	for _, grpc := range []bool{false, true} {
		for p, want := range map[kernel.Principal]string{
			{Kind: kernel.User, ID: "u-1:x"}: "user/u-1:x",
			{Kind: kernel.Service}:           "service/",
			{Kind: kernel.Anonymous}:         "anonymous/",
		} {
			got, err := ask(svc.Dial(p, scorixconnect.ConnOptions{GRPC: grpc}))
			if err != nil || got != want {
				t.Errorf("grpc=%v as %v = %q, %v; want %q", grpc, p, got, err, want)
			}
		}
	}
}

func TestACallDialedAsNobodyIsUnauthenticated(t *testing.T) {
	_, err := ask(service(t).Dial(kernel.Principal{}, scorixconnect.ConnOptions{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a call with no principal = %v, want unauthenticated", err)
	}
}

func TestAHeaderTheCallerAddsStillReachesTheService(t *testing.T) {
	m, _ := scorixconnect.NewMux(scorixconnect.Options{Auth: servertest.Auth, Timeout: 5 * time.Second})
	scorixconnect.Unary(m, whoami, kernel.Rule{}, func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("ok"), nil
	})
	var seen string
	svc := servertest.Start(t, server.Options{}, func(s *server.Server) {
		s.Mount("/test.Echo/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("X-Trace")
			m.ServeHTTP(w, r)
		}))
	})
	conn := svc.Dial(kernel.Principal{Kind: kernel.Service}, scorixconnect.ConnOptions{Header: func(_ context.Context, h http.Header) { h.Set("X-Trace", "t-1") }})
	if _, err := ask(conn); err != nil || seen != "t-1" {
		t.Fatalf("call = %v, header seen %q", err, seen)
	}
}

func TestPlainHTTPReachesTheSameServer(t *testing.T) {
	svc := service(t)
	res, err := svc.HTTPClient().Get(svc.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("healthz = %d", res.StatusCode)
	}
	if strings.HasPrefix(svc.URL, "http://127.") || strings.HasPrefix(svc.URL, "http://[::1]") {
		t.Fatalf("URL %s points at a loopback port", svc.URL)
	}
}

func TestTheServerStopsWhenTheTestEnds(t *testing.T) {
	var svc *servertest.Service
	t.Run("inner", func(t *testing.T) { svc = service(t) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, svc.URL+"/healthz", nil)
	res, err := svc.HTTPClient().Do(req)
	if err == nil {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		t.Fatalf("the server still answered %d after its test ended", res.StatusCode)
	}
	if ctx.Err() != nil {
		t.Fatalf("a call after the test ended hung instead of failing: %v", err)
	}
}

func TestTheServerIsBuiltFromTheOptionsGiven(t *testing.T) {
	var buf bytes.Buffer
	svc := servertest.Start(t, server.Options{Log: slog.New(slog.NewTextHandler(&buf, nil))}, func(*server.Server) {})
	res, err := svc.HTTPClient().Get(svc.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !strings.Contains(buf.String(), "path=/healthz") {
		t.Fatalf("the access log the options asked for is empty: %q", buf.String())
	}
}
