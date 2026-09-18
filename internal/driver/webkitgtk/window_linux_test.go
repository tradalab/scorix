//go:build linux

package webkitgtk

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/tradalab/scorix/window"
	"golang.org/x/sys/unix"
)

// The deadlock this guards: a close handler runs ON the loop thread and calls
// saveWindowState, which asks for Position. Queueing that back to a loop which
// is blocked waiting for it never returns.
func TestOnMainValRunsInlineOnTheLoopThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	old := mainTID.Load()
	mainTID.Store(int64(unix.Gettid()))
	defer mainTID.Store(old)

	// Queued instead of inline, this would wait out mainLoopWait and return 0.
	if got := onMainVal(func() int { return 42 }); got != 42 {
		t.Fatalf("onMainVal = %d, want 42: it queued instead of running inline", got)
	}
}

// GTK sets BOTH bits when a maximized window goes fullscreen, so the order the
// switch tests them in is the whole behaviour.
func TestStateFromGdk(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags int32
		want  window.State
	}{
		{"nothing set", 0, window.StateNormal},
		{"iconified", gdkStateIconified, window.StateMinimized},
		{"maximized", gdkStateMaximized, window.StateMaximized},
		{"fullscreen", gdkStateFullscreen, window.StateFullscreen},
		{"maximized then fullscreen", gdkStateMaximized | gdkStateFullscreen, window.StateFullscreen},
		{"minimized from maximized", gdkStateMaximized | gdkStateIconified, window.StateMinimized},
	} {
		if got := stateFromGdk(c.flags); got != c.want {
			t.Errorf("%s: state = %v, want %v", c.name, got, c.want)
		}
	}
}

// The e2e test sees the window; this one runs where there is no display.
func TestMaxGeometryLeavesAZeroAxisOpen(t *testing.T) {
	if g, hints := maxGeometry(600, 0); hints != gdkHintMaxSize || g.maxWidth != 600 || g.maxHeight != gdkSizeUnlimited {
		t.Errorf("maxGeometry(600, 0) = %dx%d hints %d, want 600x%d with the max hint", g.maxWidth, g.maxHeight, hints, gdkSizeUnlimited)
	}
	if _, hints := maxGeometry(0, 0); hints != 0 {
		t.Errorf("maxGeometry(0, 0) kept hints %d: clearing the cap must drop the max hint", hints)
	}
}

// gdkGeometry is handed to C by pointer, so its layout has to match GdkGeometry
// field for field. Nothing else in the build checks that.
func TestGdkGeometryLayout(t *testing.T) {
	var g gdkGeometry
	if got := unsafe.Sizeof(g); got != 56 {
		t.Errorf("sizeof = %d, want 56 (8 gint, 2 gdouble, GdkGravity, padding)", got)
	}
	for _, c := range []struct {
		name string
		off  uintptr
		want uintptr
	}{
		{"minWidth", unsafe.Offsetof(g.minWidth), 0},
		{"maxWidth", unsafe.Offsetof(g.maxWidth), 8},
		{"baseWidth", unsafe.Offsetof(g.baseWidth), 16},
		{"minAspect", unsafe.Offsetof(g.minAspect), 32},
		{"winGravity", unsafe.Offsetof(g.winGravity), 48},
	} {
		if c.off != c.want {
			t.Errorf("offset of %s = %d, want %d", c.name, c.off, c.want)
		}
	}
}
