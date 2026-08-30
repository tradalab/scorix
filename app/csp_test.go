package app

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/webview"
)

func respWith(ctype, body string) webview.SchemeHandler {
	return func(*webview.Request) *webview.Response {
		h := http.Header{}
		h.Set("Content-Type", ctype)
		return &webview.Response{Status: 200, Header: h, Body: strings.NewReader(body)}
	}
}

func newCSPApp(t *testing.T, csp string) *App {
	t.Helper()
	a, err := New(Options{URL: "scorix://app/index.html", Security: &config.SandboxConfig{CSP: csp}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestWithCSPDecoratesHTML(t *testing.T) {
	a := newCSPApp(t, "default")
	h := a.withCSP(respWith("text/html", "<html><head><title>x</title></head><body>hi</body></html>"))
	resp := h(&webview.Request{URL: "scorix://app/index.html"})

	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("CSP header = %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	metaAt := strings.Index(s, `<meta http-equiv="Content-Security-Policy"`)
	if metaAt < 0 {
		t.Fatalf("meta CSP not injected: %s", s)
	}
	if headAt := strings.Index(s, "<head>"); metaAt < headAt {
		t.Fatalf("meta injected before <head>: %s", s)
	}
	if !strings.Contains(s, "<title>x</title>") || !strings.Contains(s, "hi") {
		t.Fatalf("original HTML mangled: %s", s)
	}
}

func TestWithCSPLeavesNonHTMLAlone(t *testing.T) {
	a := newCSPApp(t, "default")
	h := a.withCSP(respWith("application/javascript", "console.log(1)"))
	resp := h(&webview.Request{URL: "scorix://app/app.js"})
	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Fatalf("CSP header on JS = %q, want none", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "console.log(1)" {
		t.Fatalf("JS body changed: %q", body)
	}
}

func TestWithCSPNonePassesThrough(t *testing.T) {
	a := newCSPApp(t, "none")
	inner := respWith("text/html", "<html></html>")
	if got := a.withCSP(inner); &got == &inner {
		t.Skip("cannot compare func identity")
	}
	resp := a.withCSP(inner)(&webview.Request{})
	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Fatalf("CSP header with none = %q", got)
	}
}

func TestHeadOpenEnd(t *testing.T) {
	cases := []struct {
		html string
		want string // substring expected right before the injected marker
	}{
		{"<html><head><title>t</title></head></html>", "<head>"},
		{`<HTML><HEAD LANG="en"><title>t</title></HEAD></HTML>`, `<HEAD LANG="en">`},
		{"<html><body><header>h</header></body></html>", ""}, // <header> must not match
	}
	for _, c := range cases {
		at := headOpenEnd([]byte(c.html))
		if c.want == "" {
			if at != -1 {
				t.Fatalf("headOpenEnd(%q) = %d, want -1", c.html, at)
			}
			continue
		}
		if at < 0 || !strings.HasSuffix(c.html[:at], ">") || !strings.Contains(c.html[:at], c.want[:5]) {
			t.Fatalf("headOpenEnd(%q) = %d", c.html, at)
		}
	}
}
