package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/internal/ipc"
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
