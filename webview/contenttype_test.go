package webview_test

import (
	"mime"
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

// The types a shell is built from are pinned, because mime.TypeByExtension
// reads the machine: the same binary answered text/javascript here and
// application/javascript on the CI image. Overriding the system entry proves
// the pin holds - a machine mapping .js to text/plain used to be enough to stop
// the shell running, with nothing to see but a blank window.
func TestContentTypeOfServedAssets(t *testing.T) {
	mime.AddExtensionType(".js", "text/plain")

	want := map[string]string{
		"index.html":    "text/html",
		"assets/app.js": "text/javascript",
		"chunk.mjs":     "text/javascript",
		"app.css":       "text/css",
		"data.json":     "application/json",
		"logo.SVG":      "image/svg+xml",
		"core.wasm":     "application/wasm",
	}
	for name, media := range want {
		got, _ := webview.SplitContentType(webview.ContentTypeOf(name))
		if got != media {
			t.Errorf("%s -> %q; want %q", name, got, media)
		}
	}
}

// Anything an app embeds beyond the shell still comes from the machine: pinning
// every extension would never end, and sniffing covers the rest.
func TestContentTypeOfDefersForTheRest(t *testing.T) {
	if err := mime.AddExtensionType(".zzz", "application/x-zzz"); err != nil {
		t.Fatal(err)
	}
	if got := webview.ContentTypeOf("blob.zzz"); got != "application/x-zzz" {
		t.Errorf("blob.zzz -> %q, want the machine's answer", got)
	}
}
