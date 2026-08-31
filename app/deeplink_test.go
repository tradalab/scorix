package app

import (
	"encoding/json"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/webview"
)

func runHeadless(t *testing.T, a *App) (stop func()) {
	t.Helper()
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
	return func() {
		a.Quit()
		<-done
	}
}

func TestLaunchArgsDispatch(t *testing.T) {
	withHeadlessDriver(t)
	old := launchArgs
	launchArgs = func() []string { return []string{"myapp://open/42", "--flag"} }
	t.Cleanup(func() { launchArgs = old })

	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.App.Protocols = []string{"myapp"}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})

	gotURL := make(chan string, 1)
	a.OnOpenURL(func(u string) {
		select {
		case gotURL <- u:
		default:
		}
	})

	stop := runHeadless(t, a)
	defer stop()

	select {
	case u := <-gotURL:
		if u != "myapp://open/42" {
			t.Fatalf("OnOpenURL got %q", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnOpenURL never fired for a launch arg")
	}
	msg := waitSentEvent(t, a.MainWindow().Window, "sys:open-url")
	var payload struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(msg.Data, &payload)
	if payload.URL != "myapp://open/42" {
		t.Fatalf("sys:open-url payload = %s", msg.Data)
	}

	// A late frontend pulls the same information over sys:launch.
	w := a.MainWindow().Window
	raw, _ := json.Marshal(webview.Message{ID: "L1", Kind: "command", Name: "sys:launch", State: "start"})
	headless.Inject(w, raw)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, sent := range headless.Sent(w) {
			var m webview.Message
			if json.Unmarshal(sent, &m) == nil && m.ID == "L1" && m.State == "done" {
				var got struct {
					URLs  []string `json:"urls"`
					Files []string `json:"files"`
				}
				_ = json.Unmarshal(m.Data, &got)
				if len(got.URLs) != 1 || got.URLs[0] != "myapp://open/42" {
					t.Fatalf("sys:launch = %s", m.Data)
				}
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("sys:launch never answered")
}

func TestFileDropDispatch(t *testing.T) {
	withHeadlessDriver(t)

	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})

	got := make(chan []string, 1)
	a.OnFileDrop(func(w *AppWindow, paths []string) {
		select {
		case got <- paths:
		default:
		}
	})

	stop := runHeadless(t, a)
	defer stop()

	headless.DropFiles(a.MainWindow().Window, 10, 20, `C:\data\a.csv`, `C:\data\b.csv`)

	select {
	case paths := <-got:
		if len(paths) != 2 || paths[0] != `C:\data\a.csv` {
			t.Fatalf("OnFileDrop paths = %v", paths)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnFileDrop never fired")
	}
	msg := waitSentEvent(t, a.MainWindow().Window, "sys:file-drop")
	var payload struct {
		Paths []string `json:"paths"`
		X     int      `json:"x"`
		Y     int      `json:"y"`
	}
	_ = json.Unmarshal(msg.Data, &payload)
	if len(payload.Paths) != 2 || payload.X != 10 || payload.Y != 20 {
		t.Fatalf("sys:file-drop payload = %s", msg.Data)
	}
}
