//go:build windows && !amd64

package webview2

// No embedded loader here; loadCreateEnv falls back to a WebView2Loader.dll on
// PATH / next to the exe.
func embeddedLoaderDLL() []byte { return nil }
