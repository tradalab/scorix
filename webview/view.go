package webview

import "context"

// View talks to JS only via OnMessage/PostMessage (no per-method binding), which
// enables structured streaming and cancellation.
type View interface {
	Navigate(url string)
	LoadHTML(html string)
	// InitScript runs before page scripts on every navigation.
	InitScript(js string)
	// Eval runs JS on the UI thread.
	Eval(js string)
	OpenDevTools() // no-op if unsupported

	OnMessage(fn func(raw []byte)) // JS->Go sink; registered once
	// PostMessage (Go->JS) is safe to call repeatedly for one id to stream chunks.
	PostMessage(raw []byte) error
}

// Capturer returns the visible part of the page as a PNG, at the device's
// pixel density. It errors when the page has not drawn yet.
type Capturer interface {
	Capture(ctx context.Context) ([]byte, error)
}
