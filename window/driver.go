package window

// Driver constructs a Runtime for one platform; backends are selected by build tag.
type Driver interface {
	Name() string // "webview2", "wkwebview", "headless"
	// NewRuntime does not start the event loop.
	NewRuntime(cfg RuntimeConfig) (Runtime, error)
}
