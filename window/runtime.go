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
