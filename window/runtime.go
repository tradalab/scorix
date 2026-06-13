package window

import "github.com/tradalab/scorix/webview"

type RuntimeConfig struct {
	Identifier     string
	SingleInstance bool
}

type RuntimeEvent string

const (
	RuntimeReady      RuntimeEvent = "ready"
	RuntimeBeforeQuit RuntimeEvent = "before-quit"
	RuntimeActivate   RuntimeEvent = "activate" // e.g. macOS dock re-open
)

// Runtime owns the OS event loop on the main thread. All window/webview
// mutations must be marshaled onto that thread via Dispatch.
type Runtime interface {
	// Run blocks until Quit. Call on main().
	Run() error
	Quit()
	// Dispatch runs fn on the UI/event-loop thread.
	Dispatch(fn func())
	Windows() Manager
	// RegisterScheme installs an in-process asset handler so app mode needs no
	// localhost HTTP server.
	RegisterScheme(scheme string, h webview.SchemeHandler)
	On(evt RuntimeEvent, fn func())
}
