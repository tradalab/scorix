//go:build linux && !android

package webkitgtk

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const otherProcessReadsClipboard = `
import gi
gi.require_version("Gtk", "3.0")
from gi.repository import Gtk, Gdk
print(Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD).wait_for_text() or "", end="")
`

func TestLiveGtkClipboardIsReadableByAnotherProcess(t *testing.T) {
	if os.Getenv("SCORIX_GTK_LIVE") == "" {
		t.Skip("set SCORIX_GTK_LIVE=1 with a display (GDK_BACKEND=x11 on Wayland, where a windowless client owns no selection)")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := initLibs(); err != nil {
		t.Skip(err)
	}
	if gtkInitCheck(0, 0) == 0 {
		t.Skip("gtk_init failed: no display")
	}
	initDispatch()
	old := mainTID.Load()
	mainTID.Store(int64(unix.Gettid()))
	defer mainTID.Store(old)

	const want = "https://s3.example.test/bucket/key?X-Amz-Signature=abc"
	type result struct {
		own, other string
		err        error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			done <- r
			dispatchMain(gtkMainQuit)
		}()
		c := gtkClipboard{}
		if r.err = c.WriteText(want); r.err != nil {
			return
		}
		if r.own, r.err = c.ReadText(); r.err != nil {
			return
		}
		out, err := exec.Command("python3", "-c", otherProcessReadsClipboard).Output()
		r.other, r.err = strings.TrimSpace(string(out)), err
	}()
	gtkMain()

	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.own != want {
		t.Fatalf("own read = %q, want %q", r.own, want)
	}
	if r.other != want {
		t.Fatalf("another process read %q, want %q", r.other, want)
	}
}
