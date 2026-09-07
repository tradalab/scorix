package webview

import (
	"mime"
	"strings"
)

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
