package window

import "github.com/tradalab/scorix/webview"

type State int

const (
	StateNormal State = iota
	StateMinimized
	StateMaximized
	StateFullscreen
)

type Window interface {
	ID() ID
	View() webview.View

	SetTitle(title string)
	SetSize(w, h int)
	Size() (w, h int)
	SetPosition(x, y int)
	Position() (x, y int)
	SetMinSize(w, h int)
	SetMaxSize(w, h int)
	Center()

	Show()
	Hide()
	Focus()
	Minimize()
	Maximize()
	Unmaximize()
	Restore()
	SetFullscreen(on bool)
	SetAlwaysOnTop(on bool)
	IsVisible() bool
	State() State

	Close()
	SetHideOnClose(on bool)

	// On handler runs on the UI thread.
	On(evt Event, fn func(EventData))
}
