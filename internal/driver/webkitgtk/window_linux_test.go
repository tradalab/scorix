//go:build linux

package webkitgtk

import (
	"runtime"
	"testing"

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
