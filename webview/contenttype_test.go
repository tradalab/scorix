package webview_test

import (
	"mime"
	"path"
	"testing"

	"github.com/tradalab/scorix/webview"
)

func TestSplitContentType(t *testing.T) {
	cases := []struct {
		ct, media, charset string
	}{
		{"text/html; charset=utf-8", "text/html", "utf-8"},
		{"text/html", "text/html", ""},
		{"application/octet-stream", "application/octet-stream", ""},
		{"", "", ""},
		{"text/html;", "text/html", ""},
		{"text/html; charset", "text/html", ""}, // malformed parameter: media type survives
	}
	for _, c := range cases {
		media, charset := webview.SplitContentType(c.ct)
		if media != c.media || charset != c.charset {
			t.Errorf("SplitContentType(%q) = %q, %q; want %q, %q", c.ct, media, charset, c.media, c.charset)
		}
	}
}

// The asset types a shell actually loads must survive as bare media types:
// WebKit matches them by exact string when the scheme handler answers.
func TestSplitContentTypeOfServedAssets(t *testing.T) {
	want := map[string]string{
		".html": "text/html",
		".js":   "text/javascript",
		".css":  "text/css",
		".json": "application/json",
	}
	for ext, media := range want {
		got, _ := webview.SplitContentType(mime.TypeByExtension(path.Ext("x" + ext)))
		if got != media {
			t.Errorf("%s -> %q; want %q", ext, got, media)
		}
	}
}
