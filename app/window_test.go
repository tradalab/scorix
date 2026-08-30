package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

// withHeadlessDriver swaps the native driver for the in-memory one so the full
// Run/OpenWindow path runs without a display.
func withHeadlessDriver(t *testing.T) {
	t.Helper()
	old := newDriver
	newDriver = headless.New
	t.Cleanup(func() { newDriver = old })
}

func waitSentEvent(t *testing.T, w window.Window, name string) webview.Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, raw := range headless.Sent(w) {
			var m webview.Message
			if json.Unmarshal(raw, &m) == nil && m.Kind == "event" && m.Name == name {
				return m
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("event %q never reached window %s", name, w.ID())
	return webview.Message{}
}

func TestOpenWindow_MultiWindowTargetedEmit(t *testing.T) {
	withHeadlessDriver(t)

	a, err := New(Options{Title: "main", URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})

	second := make(chan *AppWindow, 1)
	openErr := make(chan error, 1)
	a.OnReady(func(*App) {
		// OnReady runs on the UI thread — OpenWindow must be called off it.
		go func() {
			w, err := a.OpenWindow(window.Options{Title: "second", URL: "scorix://app/index.html"})
			openErr <- err
			second <- w
		}()
	})

	done := make(chan error, 1)
	go func() { done <- a.Run() }()

	if err := <-openErr; err != nil {
		t.Fatalf("OpenWindow: %v", err)
	}
	w2 := <-second

	if !a.EmitTo(w2.Client, "private:hello", "for-second") {
		t.Fatal("EmitTo reported the new window as disconnected")
	}
	msg := waitSentEvent(t, w2.Window, "private:hello")
	var got string
	_ = json.Unmarshal(msg.Data, &got)
	if got != "for-second" {
		t.Fatalf("payload = %q", got)
	}

	rtWins := func() []window.Window {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.rt.Windows().All()
	}()
	if len(rtWins) != 2 {
		t.Fatalf("window count = %d, want 2", len(rtWins))
	}
	a.Emit("all:ping", "x")
	for _, w := range rtWins {
		waitSentEvent(t, w, "all:ping")
	}

	// The second window's frontend can invoke commands, and its handler sees
	// the second window's ClientID.
	gotClient := make(chan ClientID, 1)
	a.Command("who", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		id, _ := ClientFrom(ctx)
		gotClient <- id
		return nil, nil
	})
	raw, _ := json.Marshal(webview.Message{ID: "1", Kind: "command", Name: "who", State: "start"})
	headless.Inject(w2.Window, raw)
	select {
	case id := <-gotClient:
		if id != w2.Client {
			t.Fatalf("handler saw client %d, want %d", id, w2.Client)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command from second window never dispatched")
	}

	a.Quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Quit")
	}
}

func TestOpenWindow_RequiresRunningRuntime(t *testing.T) {
	withHeadlessDriver(t)
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenWindow(window.Options{}); err == nil ||
		!strings.Contains(err.Error(), "app mode") {
		t.Fatalf("OpenWindow before Run must fail clearly, got: %v", err)
	}
}

type recordingModule struct{ ctx *module.Context }

func (m *recordingModule) Name() string       { return "recorder" }
func (m *recordingModule) Version() string    { return "0.0.1" }
func (m *recordingModule) Capability() string { return "recorder" }
func (m *recordingModule) OnLoad(ctx *module.Context) error {
	m.ctx = ctx
	return nil
}
func (m *recordingModule) OnStart() error  { return nil }
func (m *recordingModule) OnStop() error   { return nil }
func (m *recordingModule) OnUnload() error { return nil }

func TestSecurityWindowWiring(t *testing.T) {
	withHeadlessDriver(t)

	a, err := New(Options{
		URL:      "scorix://app/index.html",
		Security: &config.SandboxConfig{CSP: "default", AllowRightClick: false, Allowlist: config.Allowlist{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	a.cfg.Window.Debug = true // manifest window.debug
	rec := &recordingModule{}
	a.Module(rec)

	a.Show() // before Run: must be a silent no-op

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

	main := a.MainWindow()
	if main == nil {
		t.Fatal("MainWindow nil after ready")
	}
	o := headless.OptionsOf(main.Window)
	if !o.DevTools {
		t.Fatal("window.debug=true did not set Options.DevTools")
	}
	guardAt := strings.Index(o.InitScript, "contextmenu")
	bridgeAt := strings.Index(o.InitScript, "window.scorix")
	if guardAt < 0 {
		t.Fatal("allow_right_click=false left no contextmenu guard in InitScript")
	}
	if bridgeAt < 0 || guardAt > bridgeAt {
		t.Fatalf("guard must precede the bridge (guard=%d bridge=%d)", guardAt, bridgeAt)
	}
	if rec.ctx == nil || rec.ctx.App == nil {
		t.Fatal("module Context.App nil in app mode — tray Show/Quit would silently no-op")
	}

	a.Show() // with a live runtime: must not deadlock
	a.Quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Quit")
	}
}

func TestNoManifestKeepsDefaults(t *testing.T) {
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
	o := headless.OptionsOf(a.MainWindow().Window)
	if strings.Contains(o.InitScript, "contextmenu") {
		t.Fatal("no-manifest app must not inject the right-click guard")
	}
	if o.DevTools {
		t.Fatal("DevTools on without window.debug or dev mode")
	}
	a.Quit()
	<-done

	b, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	b.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	rec := &recordingModule{}
	b.Module(rec)
	_ = b.Handler() // starts modules
	defer b.stopModules()
	if rec.ctx == nil {
		t.Fatal("module never loaded via Handler")
	}
	if rec.ctx.App != nil {
		t.Fatal("Context.App must stay nil in web mode")
	}
}

func TestSystemEventsForwarded(t *testing.T) {
	withHeadlessDriver(t)

	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})

	goCalled := make(chan struct{}, 1)
	a.OnSystemEvent(window.RuntimeSuspend, func() {
		select {
		case goCalled <- struct{}{}:
		default:
		}
	})

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

	a.mu.Lock()
	rt := a.rt
	a.mu.Unlock()
	headless.Fire(rt, window.RuntimeSuspend)

	select {
	case <-goCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("OnSystemEvent handler never ran")
	}
	waitSentEvent(t, a.MainWindow().Window, "sys:suspend")

	a.Quit()
	<-done
}
