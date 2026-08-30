package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

func invokeWin(t *testing.T, w window.Window, id, name string, data string) webview.Message {
	t.Helper()
	var payload json.RawMessage
	if data != "" {
		payload = json.RawMessage(data)
	}
	raw, _ := json.Marshal(webview.Message{ID: id, Kind: "command", Name: name, State: "start", Data: payload})
	headless.Inject(w, raw)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, sent := range headless.Sent(w) {
			var m webview.Message
			if json.Unmarshal(sent, &m) == nil && m.ID == id && (m.State == "done" || m.State == "error") {
				return m
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no reply for %s", name)
	return webview.Message{}
}

func TestWindowCommands(t *testing.T) {
	withHeadlessDriver(t)

	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})

	ready := make(chan struct{}, 1)
	a.OnReady(func(*App) {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("OnReady never fired")
	}
	w := a.MainWindow().Window

	if r := invokeWin(t, w, "w1", "win:minimize", ""); r.State != "done" {
		t.Fatalf("minimize: %q %q", r.State, r.Error)
	}
	if w.State() != window.StateMinimized {
		t.Fatalf("state after minimize = %v", w.State())
	}

	r := invokeWin(t, w, "w2", "win:toggleMaximize", "")
	if r.State != "done" {
		t.Fatalf("toggleMaximize: %q %q", r.State, r.Error)
	}
	var tm struct {
		Maximized bool `json:"maximized"`
	}
	_ = json.Unmarshal(r.Data, &tm)
	if !tm.Maximized || w.State() != window.StateMaximized {
		t.Fatalf("toggleMaximize → maximized=%v state=%v", tm.Maximized, w.State())
	}

	if r := invokeWin(t, w, "w3", "win:startDrag", ""); r.State != "done" {
		t.Fatalf("startDrag: %q %q", r.State, r.Error)
	}
	if got := headless.DragCount(w); got != 1 {
		t.Fatalf("DragCount = %d, want 1", got)
	}

	if r := invokeWin(t, w, "w4", "win:setTitle", `{"title":"x"}`); r.State != "done" {
		t.Fatalf("setTitle: %q %q", r.State, r.Error)
	}

	a.Quit()
	<-done
}

func TestWindowCommandsWebModeUnavailable(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	c := dialWS(t, ts)
	defer c.Close()

	wsSend(t, c, webview.Message{ID: "1", Kind: "command", Name: "win:minimize", State: "start"})
	r := wsRecv(t, c)
	if r.State != "error" || r.ErrorCode != fault.CodeUnavailable {
		t.Fatalf("web-mode win:minimize: state=%q code=%q", r.State, r.ErrorCode)
	}
}
