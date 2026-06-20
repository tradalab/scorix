package window

// Event is delivered to On handlers on the UI thread.
type Event string

const (
	EventResize   Event = "resize"
	EventMove     Event = "move"
	EventFocus    Event = "focus"
	EventBlur     Event = "blur"
	EventMinimize Event = "minimize"
	EventMaximize Event = "maximize"
	EventClose    Event = "close" // cancelable via EventData.PreventDefault
	EventReady    Event = "ready"
)

// PreventDefault is non-nil only for cancelable events (currently EventClose) and
// vetoes the default (keeps the window open). Honored on all native drivers
// (WebView2 WM_CLOSE, wkwebview windowShouldClose:, webkitgtk delete-event) and
// headless. Only a user-initiated close is vetoable; a programmatic Close()
// proceeds to teardown regardless.
type EventData struct {
	Window         ID
	W, H           int
	X, Y           int
	PreventDefault func()
}
