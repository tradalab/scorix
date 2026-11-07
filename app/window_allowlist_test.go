package app

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

func pingFrom(t *testing.T, w window.Window, id string) webview.Message {
	t.Helper()
	raw, _ := json.Marshal(webview.Message{ID: id, Kind: "command", Name: "mod:gated:Ping", State: "start"})
	headless.Inject(w, raw)
	return waitReply(t, w, id)
}

func TestWindowAllowlistNarrowsButNeverWidens(t *testing.T) {
	withHeadlessDriver(t)
	sec := &config.SandboxConfig{
		Allowlist: config.Allowlist{"x": true},
		Windows: map[string]config.WindowSandbox{
			"viewer": {Allowlist: config.Allowlist{}},
			"main":   {Allowlist: config.Allowlist{"x": true}},
		},
	}
	a, err := New(Options{URL: "scorix://app/index.html", Security: sec})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	a.Module(&gatedModule{capability: "x"})

	opened := make(chan []*AppWindow, 1)
	a.OnReady(func(*App) {
		go func() {
			var ws []*AppWindow
			for _, id := range []window.ID{"viewer", "plain"} {
				w, err := a.OpenWindow(window.Options{ID: id, URL: "scorix://app/index.html"})
				if err != nil {
					t.Error(err)
				}
				ws = append(ws, w)
			}
			opened <- ws
		}()
	})
	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	ws := <-opened
	defer func() { a.Quit(); <-done }()

	if r := pingFrom(t, a.MainWindow().Window, "m"); r.State != "done" {
		t.Errorf("main window, granted globally and by its own list: %s %q", r.State, r.Error)
	}
	if r := pingFrom(t, ws[0].Window, "v"); r.State != "error" || r.ErrorCode != fault.CodeDenied {
		t.Errorf("viewer window, whose own list grants nothing, was let through: %s %q", r.State, r.Error)
	}
	if r := pingFrom(t, ws[1].Window, "p"); r.State != "done" {
		t.Errorf("a window with no entry of its own lost the global grant: %s %q", r.State, r.Error)
	}

	sec.Allowlist["x"] = false
	if r := pingFrom(t, a.MainWindow().Window, "w"); r.State != "error" {
		t.Errorf("a window's own list widened past a global deny: %s", r.State)
	}
}
