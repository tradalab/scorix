package app

import (
	"encoding/json"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

func newStateApp(t *testing.T, identifier string) *App {
	t.Helper()
	a, err := New(Options{URL: "scorix://app/index.html", Identifier: identifier, Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.App.Name = identifier
	a.cfg.Window.RememberState = true
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	return a
}

func TestWindowStateRoundTrip(t *testing.T) {
	withHeadlessDriver(t)
	id := "scorix-state-test"

	a := newStateApp(t, id)
	t.Cleanup(func() { _ = os.Remove(a.windowStatePath("main")) })
	_ = os.Remove(a.windowStatePath("main"))

	stop := runHeadless(t, a)
	w := a.MainWindow()
	w.SetPosition(120, 80)
	w.SetSize(1024, 700)
	stop() // Quit -> RuntimeBeforeQuit -> saveWindowState

	st, ok := a.loadWindowState("main")
	if !ok {
		t.Fatal("state file not written on quit")
	}
	if st.X != 120 || st.Y != 80 || st.W != 1024 || st.H != 700 || st.Maximized {
		t.Fatalf("saved state = %+v", st)
	}

	b := newStateApp(t, id)
	stopB := runHeadless(t, b)
	defer stopB()
	o := headless.OptionsOf(b.MainWindow().Window)
	if o.Width != 1024 || o.Height != 700 || o.X == nil || *o.X != 120 || o.Center {
		t.Fatalf("restored options = %+v", o)
	}
}

func TestWindowStateMaximizedKeepsNormalRect(t *testing.T) {
	withHeadlessDriver(t)
	id := "scorix-state-max-test"

	a := newStateApp(t, id)
	t.Cleanup(func() { _ = os.Remove(a.windowStatePath("main")) })
	_ = os.Remove(a.windowStatePath("main"))

	stop := runHeadless(t, a)
	w := a.MainWindow()
	w.SetPosition(50, 60)
	w.SetSize(900, 500)
	a.saveWindowState("main", a.MainWindow()) // snapshot the normal rect
	w.Maximize()
	stop()

	st, ok := a.loadWindowState("main")
	if !ok {
		t.Fatal("no state file")
	}
	if !st.Maximized {
		t.Fatal("maximized flag lost")
	}
	if st.W != 900 || st.H != 500 {
		t.Fatalf("normal rect clobbered by maximized save: %+v", st)
	}

	b := newStateApp(t, id)
	stopB := runHeadless(t, b)
	defer stopB()
	if b.MainWindow().State() != window.StateMaximized {
		t.Fatal("window not re-maximized on restore")
	}
}

func TestWinScreensCommand(t *testing.T) {
	withHeadlessDriver(t)
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	stop := runHeadless(t, a)
	defer stop()

	w := a.MainWindow().Window
	raw, _ := json.Marshal(webview.Message{ID: "S1", Kind: "command", Name: "win:screens", State: "start"})
	headless.Inject(w, raw)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, sent := range headless.Sent(w) {
			var m webview.Message
			if json.Unmarshal(sent, &m) == nil && m.ID == "S1" && m.State == "done" {
				var got struct {
					Screens []window.Screen `json:"screens"`
				}
				_ = json.Unmarshal(m.Data, &got)
				if len(got.Screens) != 1 || !got.Screens[0].Primary || got.Screens[0].W != 1920 {
					t.Fatalf("screens = %s", m.Data)
				}
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("win:screens never answered")
}

func TestStateOnScreen(t *testing.T) {
	screens := []window.Screen{{X: 0, Y: 0, W: 1920, H: 1080}, {X: 1920, Y: 0, W: 1280, H: 1024}}
	cases := []struct {
		st   windowState
		want bool
	}{
		{windowState{X: 100, Y: 100}, true},
		{windowState{X: 2000, Y: 500}, true},  // second monitor
		{windowState{X: 5000, Y: 100}, false}, // removed display
		{windowState{X: -900, Y: 100}, false}, // fully off the left
		{windowState{X: -30, Y: -30}, true},   // margin still on-screen
	}
	for _, c := range cases {
		if got := stateOnScreen(c.st, screens); got != c.want {
			t.Fatalf("stateOnScreen(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
	if !stateOnScreen(windowState{X: 99999}, nil) {
		t.Fatal("no screen data must trust the state")
	}
}

func TestSecondaryWindowRememberState(t *testing.T) {
	withHeadlessDriver(t)
	id := "scorix-state-secondary-test"

	open := func(a *App) *AppWindow {
		w, err := a.OpenWindow(window.Options{ID: "tools", RememberState: true, Width: 400, Height: 300})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	a := newStateApp(t, id)
	t.Cleanup(func() { _ = os.Remove(a.windowStatePath("tools")) })
	_ = os.Remove(a.windowStatePath("tools"))
	stop := runHeadless(t, a)
	w2 := open(a)
	w2.SetPosition(300, 200)
	w2.SetSize(640, 480)
	stop()

	b := newStateApp(t, id)
	stopB := runHeadless(t, b)
	defer stopB()
	o := headless.OptionsOf(open(b).Window)
	if o.Width != 640 || o.Height != 480 || o.X == nil || *o.X != 300 || o.Center {
		t.Fatalf("restored secondary options = %+v", o)
	}
}
