package headless

import (
	"testing"

	"github.com/tradalab/scorix/window"
)

func TestManagerMultiWindow(t *testing.T) {
	rt, err := New().NewRuntime(window.RuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}

	a, err := rt.Windows().New(window.Options{ID: "a", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Windows().New(window.Options{ID: "b"}); err != nil {
		t.Fatal(err)
	}

	if got := rt.Windows().Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2", got)
	}
	if _, ok := rt.Windows().Get("a"); !ok {
		t.Fatal("Get(a) not found")
	}

	var resized bool
	a.On(window.EventResize, func(window.EventData) { resized = true })
	a.SetSize(1024, 768)

	if w, h := a.Size(); w != 1024 || h != 768 {
		t.Fatalf("Size() = %dx%d, want 1024x768", w, h)
	}
	if !resized {
		t.Fatal("EventResize not fired")
	}
}

func TestWindowState(t *testing.T) {
	rt, _ := New().NewRuntime(window.RuntimeConfig{})
	w, _ := rt.Windows().New(window.DefaultOptions())

	w.Maximize()
	if w.State() != window.StateMaximized {
		t.Fatalf("State() = %d, want maximized", w.State())
	}
	w.SetFullscreen(true)
	if w.State() != window.StateFullscreen {
		t.Fatalf("State() = %d, want fullscreen", w.State())
	}
	w.Restore()
	if w.State() != window.StateNormal {
		t.Fatalf("State() = %d, want normal", w.State())
	}
}
