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

	// Every extension the five shipped shells contain, counted across 12,324
	// files: all of them must be pinned, or the machine decides again.
	want := map[string]string{
		"index.html":    "text/html",
		"assets/app.js": "text/javascript",
		"app.css":       "text/css",
		"index.txt":     "text/plain",
		"app.js.map":    "application/json",
		"inter.woff2":   "font/woff2",
		"icon.ttf":      "font/ttf",
		"favicon.ico":   "image/x-icon",
		"chunk.mjs":     "text/javascript",
		"data.json":     "application/json",
		"logo.SVG":      "image/svg+xml",
		"core.wasm":     "application/wasm",
	}
	for name, media := range want {
		got, _ := webview.SplitContentType(webview.ContentTypeOf(name, nil))
		if got != media {
			t.Errorf("%s -> %q; want %q", name, got, media)
		}
	}
}

// The tail must not read the machine either. An extension the table does not
// know is answered from the file's own bytes, so the same asset gets the same
// type wherever it is served; registering a system type for .zzz proves the OS
// table is no longer consulted at all.
func TestContentTypeOfSniffsTheTail(t *testing.T) {
	if err := mime.AddExtensionType(".zzz", "application/x-zzz"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"a.zzz", []byte("%PDF-1.7"), "application/pdf"},
		{"b.zzz", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, "image/png"},
		{"c.zzz", []byte{0x01, 0x02, 0x03, 0xFF}, "application/octet-stream"},
	}
	for _, c := range cases {
		if got, _ := webview.SplitContentType(webview.ContentTypeOf(c.name, c.data)); got != c.want {
			t.Errorf("%s -> %q, want %q", c.name, got, c.want)
		}
	}
}
