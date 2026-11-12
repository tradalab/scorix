package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
)

const echo = "/test.Echo/Say"

var anyone = kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) {
	return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.Anonymous}), nil
})

func start(t *testing.T, fn func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error), grace time.Duration) (string, context.CancelFunc, chan error) {
	t.Helper()
	m, err := scorixconnect.NewMux(scorixconnect.Options{Auth: anyone, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	scorixconnect.Unary(m, echo, kernel.Rule{}, fn)
	s := New(Options{Grace: grace, Log: slog.New(slog.DiscardHandler)})
	s.Mount("/test.Echo/", m)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	return "http://" + ln.Addr().String(), cancel, done
}

func upper(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("<" + in.GetValue() + ">"), nil
}

func TestOneRouteAnswersConnectJSONAndCleartextGRPC(t *testing.T) {
	base, _, _ := start(t, upper, 0)
	for name, conn := range map[string]*scorixconnect.Conn{
		"connect over http/1.1": scorixconnect.Dial(base, scorixconnect.ConnOptions{}),
		"grpc over h2c":         scorixconnect.Dial(base, scorixconnect.ConnOptions{GRPC: true}),
	} {
		res, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("x"))
		if err != nil || res.GetValue() != "<x>" {
			t.Errorf("%s: %v, %v", name, res, err)
		}
	}
}

func TestHealthzAnswersWithoutAuthentication(t *testing.T) {
	base, _, _ := start(t, upper, 0)
	res, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("healthz = %d, want 204", res.StatusCode)
	}
}

func TestStoppingWaitsForTheCallInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	slow := func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		close(entered)
		<-release
		return in, nil
	}
	base, stop, done := start(t, slow, 0)
	c := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](http.DefaultClient, base+echo, connect.WithProtoJSON())
	result := make(chan error, 1)
	go func() {
		_, err := c.CallUnary(context.Background(), connect.NewRequest(wrapperspb.String("x")))
		result <- err
	}()
	<-entered
	stop()
	select {
	case err := <-done:
		t.Fatalf("Run returned %v while a call was still in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("the call in flight failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run = %v after a clean stop", err)
	}
	done <- nil
}

func TestTheAccessLogNamesTheRequestButNeverItsQueryOrBody(t *testing.T) {
	var buf safeBuffer
	log, _ := observe.NewLogger(&buf, "info")
	s := New(Options{Log: log})
	s.Mount("/x", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observe.Log(r.Context()).Info("inside")
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("brew"))
	}))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	defer func() { cancel(); <-done }()
	res, err := http.Post("http://"+ln.Addr().String()+"/x?token=hunter2", "text/plain", strings.NewReader("password=hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	out := buf.String()
	for _, want := range []string{`"msg":"inside"`, `"msg":"request"`, `"method":"POST"`, `"path":"/x"`, `"status":418`, `"bytes":4`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("the access log leaked the query or body:\n%s", out)
	}
}

func TestAStatusNeverWrittenIsLoggedAsOK(t *testing.T) {
	var buf safeBuffer
	log, _ := observe.NewLogger(&buf, "info")
	s := New(Options{Log: log})
	s.Mount("/empty", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/empty", nil))
	if !strings.Contains(buf.String(), `"status":200`) || !strings.Contains(buf.String(), `"bytes":0`) {
		t.Fatalf("log = %s", buf.String())
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestTheAccessLogNamesTheGRPCCodeThatHTTPStatusHides(t *testing.T) {
	var buf safeBuffer
	log, _ := observe.NewLogger(&buf, "info")
	m, _ := scorixconnect.NewMux(scorixconnect.Options{Auth: anyone, Timeout: time.Second})
	scorixconnect.Unary(m, echo, kernel.Rule{}, func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		if in.GetValue() == "missing" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("gone"))
		}
		return in, nil
	})
	s := New(Options{Log: log})
	s.Mount("/test.Echo/", m)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	defer func() { cancel(); <-done }()
	conn := scorixconnect.Dial("http://"+ln.Addr().String(), scorixconnect.ConnOptions{GRPC: true})
	scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("missing"))
	scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("x"))
	out := buf.String()
	if !strings.Contains(out, `"status":200,`) || !strings.Contains(out, `"code":"not_found"`) || !strings.Contains(out, `"code":"ok"`) {
		t.Fatalf("log = %s", out)
	}
	plain := httptest.NewRecorder()
	New(Options{Log: log}).handler().ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if lines := strings.Split(strings.TrimSpace(buf.String()), "\n"); strings.Contains(lines[len(lines)-1], `"code"`) {
		t.Fatalf("a plain HTTP request was given a gRPC code: %s", lines[len(lines)-1])
	}
}

func TestACallOverTheInFlightCapIsShedAsUnavailableButHealthzAndAProbeStillAnswer(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	slow := func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return in, nil
	}
	m, _ := scorixconnect.NewMux(scorixconnect.Options{Auth: anyone, Timeout: 10 * time.Second})
	scorixconnect.Unary(m, echo, kernel.Rule{}, slow)
	var shed atomic.Int32
	s := New(Options{MaxInFlight: 2, OnShed: func(r *http.Request) {
		if r.URL.Path == echo {
			shed.Add(1)
		}
	}})
	s.Mount("/test.Echo/", m)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	s.Probe("GET /api/v1/healthz", ok)
	s.Mount("GET /api/v1/other", ok)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	defer func() { cancel(); <-done }()
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	base := "http://" + ln.Addr().String()
	held := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), scorixconnect.Dial(base, scorixconnect.ConnOptions{}), echo, wrapperspb.String("x"))
			held <- err
		}()
		select {
		case <-entered:
		case err := <-held:
			t.Fatalf("a call under the cap = %v", err)
		}
	}
	for name, conn := range map[string]*scorixconnect.Conn{
		"connect": scorixconnect.Dial(base, scorixconnect.ConnOptions{}),
		"grpc":    scorixconnect.Dial(base, scorixconnect.ConnOptions{GRPC: true}),
	} {
		bounded, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](bounded, conn, echo, wrapperspb.String("x"))
		stop()
		if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Errorf("%s over the cap = %v, want unavailable", name, err)
		}
	}
	if n := shed.Load(); n != 2 {
		t.Errorf("OnShed heard %d sheds, want 2", n)
	}
	res, err := http.Get(base + "/healthz")
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("healthz under load = %v, %v", res, err)
	}
	res.Body.Close()
	for path, want := range map[string]int{"/api/v1/healthz": http.StatusOK, "/api/v1/other": http.StatusServiceUnavailable} {
		res, err := http.Get(base + path)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s under load = %v, %v; want %d", path, res, err, want)
		}
		res.Body.Close()
	}
	free()
	for range 2 {
		if err := <-held; err != nil {
			t.Fatalf("a call under the cap failed: %v", err)
		}
	}
	if _, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), scorixconnect.Dial(base, scorixconnect.ConnOptions{}), echo, wrapperspb.String("x")); err != nil {
		t.Fatalf("a call after the load drained = %v", err)
	}
}

func TestAMountsOutersWrapItOutsideSheddingFirstOutermostSoAShedCallAndAPreflightCarryTheirHeaders(t *testing.T) {
	tag := func(name string) func(http.Handler) http.Handler {
		return func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Add("X-Outer", name)
				h.ServeHTTP(w, r)
			})
		}
	}
	cors := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s := New(Options{MaxInFlight: 1})
	s.Mount("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
	}), tag("first"), cors, tag("last"))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	s.Mount("/plain/", ok)
	s.Probe("GET /api/v1/healthz", ok)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	held := make(chan error, 1)
	go func() {
		res, err := http.Post(srv.URL+"/api/a", "text/plain", nil)
		if err == nil {
			res.Body.Close()
		}
		held <- err
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-held; err != nil {
			t.Errorf("the call holding the slot = %v", err)
		}
	}()
	for _, c := range []struct {
		method, path string
		status       int
		outers       string
		cors         bool
	}{
		{"POST", "/api/b", http.StatusServiceUnavailable, "[first last]", true},
		{"OPTIONS", "/api/b", http.StatusNoContent, "[first]", true},
		{"GET", "/plain/x", http.StatusServiceUnavailable, "[]", false},
		{"GET", "/healthz", http.StatusNoContent, "[]", false},
		{"GET", "/api/v1/healthz", http.StatusOK, "[]", false},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.status || fmt.Sprint(res.Header.Values("X-Outer")) != c.outers || (res.Header.Get("Access-Control-Allow-Origin") == "*") != c.cors {
			t.Errorf("%s %s under load = %d %v, want %d outers %s CORS %v", c.method, c.path, res.StatusCode, res.Header, c.status, c.outers, c.cors)
		}
	}
}

func TestWithTLSOneRouteAnswersConnectAndGRPCOverTLSAndRefusesCleartext(t *testing.T) {
	borrowed := httptest.NewUnstartedServer(http.NotFoundHandler())
	borrowed.EnableHTTP2 = true
	borrowed.StartTLS()
	certs, trust := borrowed.TLS.Certificates, borrowed.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	borrowed.Close()

	m, _ := scorixconnect.NewMux(scorixconnect.Options{Auth: anyone, Timeout: 5 * time.Second})
	scorixconnect.Unary(m, echo, kernel.Rule{}, func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("<" + in.GetValue() + ">"), nil
	})
	s := New(Options{TLS: &tls.Config{Certificates: certs}})
	s.Mount("/test.Echo/", m)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	defer func() { cancel(); <-done }()
	addr := ln.Addr().String()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: trust, ForceAttemptHTTP2: true}}
	for name, conn := range map[string]*scorixconnect.Conn{
		"connect": scorixconnect.Dial("https://"+addr, scorixconnect.ConnOptions{HTTPClient: client}),
		"grpc":    scorixconnect.Dial("https://"+addr, scorixconnect.ConnOptions{GRPC: true, HTTPClient: client}),
	} {
		res, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("x"))
		if err != nil || res.GetValue() != "<x>" {
			t.Errorf("%s over TLS = %v, %v", name, res, err)
		}
	}
	res, err := client.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.ProtoMajor != 2 {
		t.Errorf("a TLS client got %s, want HTTP/2 offered over ALPN", res.Proto)
	}
	plain := scorixconnect.Dial("http://"+addr, scorixconnect.ConnOptions{GRPC: true})
	if _, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), plain, echo, wrapperspb.String("x")); err == nil {
		t.Fatal("a TLS server answered a cleartext gRPC call")
	}
}

func TestWithTheAccessLogAStreamCanStillFlushAndReachTheConnection(t *testing.T) {
	release := make(chan struct{})
	s := New(Options{Log: slog.New(slog.DiscardHandler)})
	s.Mount("/s", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte("a"))
		w.(http.Flusher).Flush()
		<-release
	}))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	defer func() { cancel(); <-done }()
	defer close(release)
	first := make(chan string, 1)
	go func() {
		res, err := http.Get("http://" + ln.Addr().String() + "/s")
		if err != nil {
			first <- err.Error()
			return
		}
		defer res.Body.Close()
		b := make([]byte, 1)
		n, _ := res.Body.Read(b)
		first <- fmt.Sprint(res.StatusCode, " ", string(b[:n]))
	}()
	select {
	case got := <-first:
		if got != "200 a" {
			t.Fatalf("got %q, want 200 a", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flushed byte never reached the caller")
	}
}

func TestAStreamThatOutlivesTheGraceIsCancelledOrCutOffAndRunSaysSo(t *testing.T) {
	for name, grpc := range map[string]bool{"http/1.1": false, "h2c": true} {
		var entered sync.WaitGroup
		waited, spammed := make(chan struct{}), make(chan struct{})
		m, _ := scorixconnect.NewMux(scorixconnect.Options{Auth: anyone, Timeout: time.Hour})
		scorixconnect.ServerStream(m, "/test.Echo/Wait", kernel.Rule{}, func(ctx context.Context, _ *wrapperspb.StringValue, send func(*wrapperspb.StringValue) error) error {
			send(wrapperspb.String("a"))
			entered.Done()
			<-ctx.Done()
			close(waited)
			return ctx.Err()
		})
		scorixconnect.ServerStream(m, "/test.Echo/Spam", kernel.Rule{}, func(_ context.Context, _ *wrapperspb.StringValue, send func(*wrapperspb.StringValue) error) error {
			send(wrapperspb.String("a"))
			entered.Done()
			for send(wrapperspb.String("a")) == nil {
				time.Sleep(10 * time.Millisecond)
			}
			close(spammed)
			return nil
		})
		s := New(Options{Grace: 100 * time.Millisecond})
		s.Mount("/test.Echo/", m)
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx, ln) }()
		conn := scorixconnect.Dial("http://"+ln.Addr().String(), scorixconnect.ConnOptions{GRPC: grpc})
		entered.Add(2)
		for _, p := range []string{"/test.Echo/Wait", "/test.Echo/Spam"} {
			go scorixconnect.Stream(context.Background(), conn, p, wrapperspb.String("x"), func(*wrapperspb.StringValue) error { return nil })
		}
		entered.Wait()
		cancel()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "grace") {
				t.Errorf("%s: Run = %v, want it to say the grace ran out", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Run did not return", name)
		}
		for what, ended := range map[string]chan struct{}{"waiting on its context": waited, "ignoring its context": spammed} {
			select {
			case <-ended:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: the stream %s kept running after Run returned", name, what)
			}
		}
	}
}
