//go:build windows

package webview2

import (
	goruntime "runtime"
	"testing"
	"unsafe"

	"github.com/tradalab/scorix/window"
)

// TestEnvironmentOptionsAccepted creates a real WebView2 environment with the
// custom-scheme options object and asserts the installed runtime accepts it.
func TestEnvironmentOptionsAccepted(t *testing.T) {
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()

	if _, err := loadCreateEnv(); err != nil {
		t.Skipf("WebView2 runtime/loader unavailable: %v", err)
	}
	coInitSTA()

	// Message-only window suffices: CreateCoreWebView2EnvironmentWithOptions
	// validates the options object synchronously, before the hwnd is used.
	hwnd, err := createWindow(window.Options{}, true)
	if err != nil {
		t.Fatalf("createWindow: %v", err)
	}

	var pinned []*handler // keep callbacks alive past the (queued) async bring-up
	track := func(h *handler) *handler { pinned = append(pinned, h); return h }

	err = createEnvironment(hwnd, "", []string{"scorix"}, track, nil, func(_, _, _ unsafe.Pointer) {})
	if err != nil {
		t.Fatalf("WebView2 rejected the environment options object: %v\n"+
			"a hand-rolled ICoreWebView2EnvironmentOptions getter returns a value the runtime rejects "+
			"(see scheme_register_windows.go)", err)
	}
	_ = pinned
}
