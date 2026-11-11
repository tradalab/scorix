package window

import "github.com/tradalab/scorix/webview"

type RuntimeConfig struct {
	Identifier string
	// Keeps web content out of the OS sandbox the driver would put it in
	// (bubblewrap on Linux). Drivers without one ignore it.
	NoWebviewSandbox bool
	// OpenExternal gets the URL a person clicked toward when a window refuses to leave its
	// origin; the window stays where it was. Nil drops such clicks.
	OpenExternal func(url string)
	// FrameSources are the CSP frame sources a window may still load from another origin,
	// since a driver that cannot tell a frame from the window lets them through.
	FrameSources []string
}

type RuntimeEvent string

const (
	RuntimeReady      RuntimeEvent = "ready"
	RuntimeBeforeQuit RuntimeEvent = "before-quit"
	RuntimeActivate   RuntimeEvent = "activate" // e.g. macOS dock re-open

	RuntimeThemeChanged RuntimeEvent = "theme-changed" // OS light/dark flipped
	RuntimeSuspend      RuntimeEvent = "suspend"       // system is going to sleep
	RuntimeResume       RuntimeEvent = "resume"        // system woke up

	// Mobile only. Background is the last reliable moment to persist: the OS may kill the app after it.
	RuntimeBackground RuntimeEvent = "background"
	RuntimeForeground RuntimeEvent = "foreground"
	RuntimeLowMemory  RuntimeEvent = "low-memory"
)

// URLOpener exists because Android delivers a tapped link to the live Activity, not in argv.
type URLOpener interface {
	OnOpenURL(fn func(url string))
}

// Runtime owns the OS event loop on the main thread; all window/webview mutations
// must be marshaled onto it via Dispatch.
type Runtime interface {
	Run() error // blocks until Quit; call on main()
	Quit()
	Dispatch(fn func()) // runs fn on the UI/event-loop thread
	Windows() Manager
	// RegisterScheme installs an in-process asset handler (no localhost server).
	RegisterScheme(scheme string, h webview.SchemeHandler)
	On(evt RuntimeEvent, fn func())
}
