package mobilebridge

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

type sent struct {
	op   int
	data string
}

type fakeHost struct {
	mu  sync.Mutex
	ops []sent
}

func (h *fakeHost) send(op int, data []byte) {
	h.mu.Lock()
	h.ops = append(h.ops, sent{op, string(data)})
	h.mu.Unlock()
}

func (h *fakeHost) count(op int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.ops {
		if s.op == op {
			n++
		}
	}
	return n
}

var sameURLs = URLs{
	ToWeb:   func(u string) string { return u },
	FromWeb: func(u string) (string, bool) { return u, true },
	Origin: func(u string) string {
		scheme, rest, _ := strings.Cut(u, "://")
		host, _, _ := strings.Cut(rest, "/")
		return scheme + "://" + host
	},
}

func iosRuntime(t *testing.T) (*runtime, *fakeHost) {
	t.Helper()
	h := &fakeHost{}
	rt, err := New("ioswebview", sameURLs, h.send).NewRuntime(window.RuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*runtime)
	active.Store(r)
	t.Cleanup(func() { active.Store(nil) })
	return r, h
}

func TestDispatchWakesTheHostOncePerBatchAndRunTasksDrainsIt(t *testing.T) {
	r, h := iosRuntime(t)
	var ran []int
	r.Dispatch(func() { ran = append(ran, 1) })
	r.Dispatch(func() { ran = append(ran, 2) })
	if got := h.count(OpWake); got != 1 {
		t.Fatalf("wakes = %d, want 1 for two tasks queued before the host ran them", got)
	}
	RunTasks()
	if len(ran) != 2 || ran[0] != 1 || ran[1] != 2 {
		t.Fatalf("ran = %v, want both tasks in order", ran)
	}
	r.Dispatch(func() {})
	if got := h.count(OpWake); got != 2 {
		t.Fatalf("wakes = %d, want a new wake once the queue had drained", got)
	}
}

func TestOpeningTheWindowTellsTheHostItsURLAndOriginAndASecondOneIsRefused(t *testing.T) {
	r, h := iosRuntime(t)
	if _, err := r.Windows().New(window.Options{URL: "scorix://app/index.html", Title: "T"}); err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	for _, s := range h.ops {
		if s.op == OpOpen {
			_ = json.Unmarshal([]byte(s.data), &spec)
		}
	}
	if spec["url"] != "scorix://app/index.html" || spec["origin"] != "scorix://app" || spec["title"] != "T" {
		t.Fatalf("open spec = %v", spec)
	}
	if _, err := r.Windows().New(window.Options{}); err == nil {
		t.Fatal("a second window opened on a phone")
	}
}

func TestAPageMessageReachesTheWindowsSink(t *testing.T) {
	r, _ := iosRuntime(t)
	w, _ := r.Windows().New(window.Options{URL: "scorix://app/"})
	var got string
	w.View().OnMessage(func(raw []byte) { got = string(raw) })
	Message([]byte(`{"id":"1"}`))
	if got != `{"id":"1"}` {
		t.Fatalf("sink got %q", got)
	}
}

func TestARequestReachesTheSchemeHandlerAndComesBackPacked(t *testing.T) {
	r, _ := iosRuntime(t)
	r.RegisterScheme("scorix", func(req *webview.Request) *webview.Response {
		if req.URL != "scorix://app/hello.txt" || req.Header.Get("Range") != "bytes=0-1" {
			return &webview.Response{Status: http.StatusBadRequest}
		}
		h := http.Header{}
		h.Set("Content-Type", "text/plain")
		return &webview.Response{Status: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("hi"))}
	})
	out := string(ServeRequest([]byte("GET scorix://app/hello.txt\nRange: bytes=0-1")))
	if out != "200 OK\nContent-Type: text/plain\n\nhi" {
		t.Fatalf("packed = %q", out)
	}
	if out := string(ServeRequest([]byte("GET other://x/y"))); !strings.HasPrefix(out, "404 ") {
		t.Fatalf("unregistered scheme answered %q", out)
	}
}

func TestLifecycleCodesBecomeRuntimeEvents(t *testing.T) {
	r, _ := iosRuntime(t)
	var got []window.RuntimeEvent
	for _, e := range []window.RuntimeEvent{window.RuntimeForeground, window.RuntimeBackground, window.RuntimeLowMemory} {
		r.On(e, func() { got = append(got, e) })
	}
	Lifecycle(LifeBackground)
	Lifecycle(LifeForeground)
	Lifecycle(LifeLowMemory)
	Lifecycle(99)
	want := []window.RuntimeEvent{window.RuntimeBackground, window.RuntimeForeground, window.RuntimeLowMemory}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestOnACustomSchemeTheAppsOwnPagesStayAndAForeignSiteIsKeptOut(t *testing.T) {
	r, _ := iosRuntime(t)
	if _, err := r.Windows().New(window.Options{URL: "scorix://app/index.html"}); err != nil {
		t.Fatal(err)
	}
	if KeepsOut("scorix://app/settings.html", false) {
		t.Error("the app's own page was kept out of its window")
	}
	if !KeepsOut("https://example.com/", false) {
		t.Error("a foreign site was let into the app's window")
	}
}
