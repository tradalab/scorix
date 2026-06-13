package webview

// View talks to JS only via OnMessage/PostMessage (no per-method binding), which
// is what enables structured streaming and cancellation.
type View interface {
	// Navigate URL, typically a custom scheme, e.g. scorix://app/index.html.
	Navigate(url string)
	LoadHTML(html string)
	// InitScript JS is injected before page scripts on every navigation.
	InitScript(js string)
	// Eval runs JS on the UI thread.
	Eval(js string)
	// OpenDevTools is a no-op if unsupported.
	OpenDevTools()

	// OnMessage registers the JS -> Go sink. Called once at wiring time.
	OnMessage(fn func(raw []byte))
	// PostMessage sends Go -> JS. Safe to call repeatedly for one request id to stream chunks.
	PostMessage(raw []byte) error
}
