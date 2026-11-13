package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradalab/scorix/observe"
)

func holdOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("data: hi\n\n"))
	http.NewResponseController(w).Flush()
	<-r.Context().Done()
}

func runServer(t *testing.T, s *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	return "http://" + ln.Addr().String()
}

func openStream(t *testing.T, url string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode == http.StatusOK {
		line, err := bufio.NewReader(res.Body).ReadString('\n')
		if err != nil || line != "data: hi\n" {
			t.Fatalf("first event = %q, %v", line, err)
		}
	}
	return res, func() { cancel(); res.Body.Close() }
}

func TestAnOpenStreamHoldsNoInFlightSlotSoShortCallsStillAnswer(t *testing.T) {
	s := New(Options{MaxInFlight: 1})
	s.Stream("GET /events", http.HandlerFunc(holdOpen), StreamOptions{})
	s.Mount("GET /short", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	base := runServer(t, s)
	for range 2 {
		_, closeIt := openStream(t, base+"/events")
		defer closeIt()
	}
	res, err := http.Get(base + "/short")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a short call beside two open streams = %d, want 200", res.StatusCode)
	}
}

func TestAStreamOverMaxConnsIsRefusedWith503AndRetryAfterUntilOneCloses(t *testing.T) {
	var shed atomic.Int32
	s := New(Options{OnShed: func(*http.Request) { shed.Add(1) }})
	s.Stream("GET /events", http.HandlerFunc(holdOpen), StreamOptions{MaxConns: 2})
	base := runServer(t, s)
	_, first := openStream(t, base+"/events")
	defer first()
	_, second := openStream(t, base+"/events")
	defer second()
	over, closeOver := openStream(t, base+"/events")
	closeOver()
	if over.StatusCode != http.StatusServiceUnavailable || over.Header.Get("Retry-After") == "" {
		t.Fatalf("a stream over the cap = %d Retry-After %q, want 503 with Retry-After", over.StatusCode, over.Header.Get("Retry-After"))
	}
	if n := shed.Load(); n != 1 {
		t.Fatalf("OnShed heard %d, want 1", n)
	}
	first()
	deadline := time.Now().Add(2 * time.Second)
	for {
		res, closeIt := openStream(t, base+"/events")
		closeIt()
		if res.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a stream after one closed = %d, want 200", res.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAStreamsOutersWrapItOutsideTheCapSoARefusedStreamCarriesTheirHeaders(t *testing.T) {
	cors := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			h.ServeHTTP(w, r)
		})
	}
	s := New(Options{})
	s.Stream("GET /events", http.HandlerFunc(holdOpen), StreamOptions{MaxConns: 1}, cors)
	base := runServer(t, s)
	_, held := openStream(t, base+"/events")
	defer held()
	over, closeOver := openStream(t, base+"/events")
	closeOver()
	if over.StatusCode != http.StatusServiceUnavailable || over.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("a refused stream = %d %v, want 503 with the outer's CORS header", over.StatusCode, over.Header)
	}
}

func TestAStreamIsLoggedOnceWhenItClosesWithItsDuration(t *testing.T) {
	var buf safeBuffer
	log, _ := observe.NewLogger(&buf, "info")
	s := New(Options{Log: log})
	s.Stream("GET /events", http.HandlerFunc(holdOpen), StreamOptions{})
	base := runServer(t, s)
	_, closeIt := openStream(t, base+"/events")
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(buf.String(), `"path":"/events"`) {
		t.Fatalf("an open stream was logged before it closed:\n%s", buf.String())
	}
	closeIt()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), `"path":"/events"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no access line after the stream closed:\n%s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"stream"`, `"duration":`, `"status":200`} {
		if !strings.Contains(out, want) {
			t.Errorf("stream line lacks %s:\n%s", want, out)
		}
	}
}
