//go:build windows && amd64

package webview2

import _ "embed"

// Bundled so apps ship as a single .exe. This is the Microsoft redistributable
// loader, licensed for redistribution.
//
//go:embed webview2loader/amd64/WebView2Loader.dll
var embeddedLoader []byte

func embeddedLoaderDLL() []byte { return embeddedLoader }
