package webview

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// shellTypes pins the types a static export is built from. mime.TypeByExtension
// answers from the machine's own table - /etc/mime.types, the Windows registry -
// so the served type of an app's OWN embedded assets varied by end-user machine:
// .js came back application/javascript on the CI image and text/javascript here,
// and a table saying text/plain would stop the shell running at all.
// It covers every extension the five shipped shells contain, counted rather than
// guessed - .txt among them is not a stray readme but the RSC payload the router
// fetches on navigation - plus what a public/ folder routinely adds.
var shellTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".map":   "application/json",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".ico":   "image/x-icon",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".wasm":  "application/wasm",
	".woff":  "font/woff",
	".webp":  "image/webp",
	".png":   "image/png",
}

// PinnedContentType reports what this build serves for name, and whether the
// table knows the extension at all. scorix build reads the second return to name
// what a shell ships that nothing pins.
func PinnedContentType(name string) (string, bool) {
	ct, ok := shellTypes[strings.ToLower(path.Ext(name))]
	return ct, ok
}

// ContentTypeOf falls back to sniffing the BYTES, never mime.TypeByExtension:
// that reads the machine, so the tail would keep the very inconsistency the pin
// exists to remove. DetectContentType reads magic numbers, so pdf/png/wasm come
// out right and anything unrecognisable comes out octet-stream - the same answer
// on every machine either way.
func ContentTypeOf(name string, data []byte) string {
	if ct, ok := PinnedContentType(name); ok {
		return ct
	}
	return http.DetectContentType(data)
}

// SplitContentType separates "text/html; charset=utf-8" into the bare media
// type and its charset. WKWebView carries the two in separate NSURLResponse
// fields and matches the media type by exact string, so a type that still has
// its parameters attached misses "text/html" and falls through to WebKit's
// plain-text document, which paints index.html as source instead of rendering
// it.
func SplitContentType(ct string) (mediaType, charset string) {
	if mt, params, err := mime.ParseMediaType(ct); err == nil {
		return mt, params["charset"]
	}
	// Malformed parameters must not drag the media type down with them.
	mediaType, _, _ = strings.Cut(ct, ";")
	return strings.TrimSpace(mediaType), ""
}
