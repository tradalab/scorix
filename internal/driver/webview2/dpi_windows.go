//go:build windows

package webview2

import "sync"

var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAware            = user32.NewProc("SetProcessDpiAware")
	procGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
)

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (HANDLE)-4.
const dpiAwarenessContextPerMonitorV2 = ^uintptr(3)

var dpiAwarenessOnce sync.Once

// enableDPIAwareness opts the process into Per-Monitor-V2 DPI awareness so
// Windows stops bitmap-stretching our windows on scaled displays — that stretch
// is what makes WebView2 content look blurry at any scale above 100%. Must run
// before the first top-level window is created. Falls back to system-DPI
// awareness on pre-1703 Windows where the context API is absent. Idempotent.
func enableDPIAwareness() {
	dpiAwarenessOnce.Do(func() {
		if procSetProcessDpiAwarenessContext.Find() == nil {
			if r, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorV2); r != 0 {
				return
			}
		}
		procSetProcessDpiAware.Call() // Vista+ fallback: system-DPI aware
	})
}

// windowDPI returns the window's effective DPI (defaultDPI = 100%). Falls back
// to defaultDPI on pre-1607 Windows where GetDpiForWindow is unavailable.
func (w *win) windowDPI() int {
	if procGetDpiForWindow.Find() != nil {
		return defaultDPI
	}
	d, _, _ := procGetDpiForWindow.Call(uintptr(w.hwnd))
	if d == 0 {
		return defaultDPI
	}
	return int(d)
}
