package webview_test

import (
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tradalab/scorix/webview"
)

func TestSchemeFromFS(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>root</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := webview.SchemeFromFS(fsys)

	body := func(r *webview.Response) string {
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}

	t.Run("root serves index", func(t *testing.T) {
		r := h(&webview.Request{Method: "GET", URL: "scorix://app/"})
		if r.Status != 200 || body(r) != "<html>root</html>" {
			t.Fatalf("status=%d body=%q", r.Status, body(r))
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "html") {
			t.Fatalf("content-type=%q", ct)
		}
	})

	t.Run("nested asset with content-type", func(t *testing.T) {
		r := h(&webview.Request{URL: "scorix://app/assets/app.js"})
		if r.Status != 200 || body(r) != "console.log(1)" {
			t.Fatalf("status=%d body=%q", r.Status, body(r))
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Fatalf("content-type=%q", ct)
		}
	})

	t.Run("missing asset 404", func(t *testing.T) {
		r := h(&webview.Request{URL: "scorix://app/missing.png"})
		if r.Status != 404 {
			t.Fatalf("status=%d, want 404", r.Status)
		}
	})

	t.Run("spa route falls back to index", func(t *testing.T) {
		r := h(&webview.Request{URL: "scorix://app/dashboard"})
		if r.Status != 200 || body(r) != "<html>root</html>" {
			t.Fatalf("status=%d body=%q", r.Status, body(r))
		}
	})

	t.Run("path traversal blocked", func(t *testing.T) {
		r := h(&webview.Request{URL: "scorix://app/../secret"})
		// cleaned to index fallback, never escapes fsys
		if r.Status != 200 {
			t.Fatalf("status=%d", r.Status)
		}
	})
}
