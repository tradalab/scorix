//go:build linux && !android

package webkitgtk

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tradalab/scorix/logger"
)

const sandboxProbeWait = 3 * time.Second

// SandboxUsable reports why WebKit could not wrap its web process in bubblewrap here,
// or nil when it can. The driver asks once at startup and runs web content unsandboxed,
// with a warning, rather than leave a window that never paints.
func SandboxUsable() error {
	if _, err := os.Stat("/.flatpak-info"); err == nil {
		return nil
	}
	for _, bin := range []string{"bwrap", "xdg-dbus-proxy"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s is not installed", bin)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bwrap",
		"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent",
		"true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bwrap cannot create a sandbox here: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func enableSandbox(optedOut bool) {
	if optedOut {
		logger.Warn("webkitgtk: web content runs unsandboxed because security.webview_sandbox is false")
		return
	}
	if err := SandboxUsable(); err != nil {
		logger.Warn("webkitgtk: web content runs unsandboxed", "reason", err.Error())
		return
	}
	wkCtxSetSandbox(wkCtxDefault(), 1)
}
