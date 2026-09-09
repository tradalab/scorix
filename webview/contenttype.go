package webview

import (
	"mime"
	"path"
	"strings"
)

// shellTypes pins the types a static export is built from. mime.TypeByExtension
// answers from the machine's own table - /etc/mime.types, the Windows registry -
// so the served type of an app's OWN embedded assets varied by end-user machine:
// .js came back application/javascript on the CI image and text/javascript here,
// and a table saying text/plain would stop the shell running at all.
var shellTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".mjs":  "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json",
	".svg":  "image/svg+xml",
	".wasm": "application/wasm",
}

// ContentTypeOf answers for what a shell is made of and defers to the machine
// for the rest, where sniffing is harmless and the list would never end.
func ContentTypeOf(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := shellTypes[ext]; ok {
		return ct
	}
	return mime.TypeByExtension(ext)
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
