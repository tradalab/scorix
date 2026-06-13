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

// EventData carries window event details. PreventDefault is non-nil only for
// cancelable events (currently EventClose).
type EventData struct {
	Window         ID
	W, H           int
	X, Y           int
	PreventDefault func()
}
