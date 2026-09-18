//go:build linux

package webkitgtk

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Needs a display, so it rides with the e2e job, where SCORIX_E2E says there is one.
func TestWindowIconLoadsTheIcoEveryAppShips(t *testing.T) {
	if os.Getenv("SCORIX_E2E") == "" {
		t.Skip("set SCORIX_E2E=1 on a machine with a display")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := initLibs(); err != nil {
		t.Fatal(err)
	}
	if gtkInitCheck(0, 0) == 0 {
		t.Fatal("gtk_init_check failed: no display")
	}
	gw := gtkWindowNew(gtkWindowToplevel)
	defer gtkWidgetDestroy(gw)

	// The scaffold's own icon.ico, so the format apps really ship is the one decoded.
	ico := filepath.Join("..", "..", "cli", "template", "static", "project", "assets", "icon.ico")
	if !setWindowIcon(gw, ico) {
		t.Fatal("GTK did not take the scaffold's icon.ico as the window icon")
	}
	if setWindowIcon(gw, filepath.Join(t.TempDir(), "missing.ico")) {
		t.Fatal("a missing icon file reported success")
	}
}
