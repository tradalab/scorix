package app

import (
	"io"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/tradalab/scorix/webview"
)

func TestServeBlobAppScheme(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}}

	blob := a.ServeBlob("application/octet-stream", []byte{0x00, 0x01, 0xFF})
	h := a.schemeWithBlobs(webview.SchemeFromFS(fsys))

	resp := h(&webview.Request{URL: "scorix://app" + blob.URL()})
	if resp.Status != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("blob resp = %d %q", resp.Status, resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "\x00\x01\xff" {
		t.Fatalf("blob body = %v", body)
	}

	page := h(&webview.Request{URL: "scorix://app/index.html"})
	pageBody, _ := io.ReadAll(page.Body)
	if page.Status != 200 || !containsBytes(pageBody, "Content-Security-Policy") {
		t.Fatalf("asset path lost CSP decoration: %d %s", page.Status, pageBody)
	}

	blob.Close()
	if gone := h(&webview.Request{URL: "scorix://app" + blob.URL()}); gone.Status != 404 {
		t.Fatalf("closed blob status = %d, want 404", gone.Status)
	}
}

func containsBytes(b []byte, s string) bool { return string(b) != "" && stringContains(string(b), s) }

func stringContains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestServeBlobWebMode(t *testing.T) {
	a := newTestApp(t)
	blob := a.ServeBlob("image/png", []byte("PNGDATA"))
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	res, err := ts.Client().Get(ts.URL + blob.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" || string(body) != "PNGDATA" {
		t.Fatalf("web blob = %d %q %q", res.StatusCode, res.Header.Get("Content-Type"), body)
	}

	blob.Close()
	res2, _ := ts.Client().Get(ts.URL + blob.URL())
	res2.Body.Close()
	if res2.StatusCode != 404 {
		t.Fatalf("closed blob = %d, want 404", res2.StatusCode)
	}
}

func TestServeBlobWebModeAuthGated(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html", WebToken: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	blob := a.ServeBlob("text/plain", []byte("secret bytes"))
	defer blob.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	res, _ := ts.Client().Get(ts.URL + blob.URL())
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("blob without token = %d, want 401", res.StatusCode)
	}
}
