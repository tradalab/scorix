package origin

import (
	"strconv"
	"strings"
	"testing"
)

func TestOf(t *testing.T) {
	for raw, want := range map[string]string{
		"scorix://app/index.html?x=1#y": "scorix://app",
		"SCORIX://App/a.js":             "scorix://app",
		"http://localhost:7001/":        "http://localhost:7001",
		"https://example.com/login":     "https://example.com",
		"https://example.com:443/":      "https://example.com",
		"http://example.com:80/":        "http://example.com",
		"https://example.com:8443/":     "https://example.com:8443",
		"":                              "about:blank",
		"about:srcdoc":                  "about:blank",
		"data:text/html,hi":             "data:",
		"::not a url":                   "",
		"blob:https://Evil.example/u-1": "https://evil.example",
		"blob:":                         "blob:",
	} {
		if got := Of(raw); got != want {
			t.Errorf("Of(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestGuardAcceptsOnlyTheDocumentGoLoaded(t *testing.T) {
	var g Guard
	g.Trust("scorix://app/index.html")
	if !g.Allows("scorix://app/settings/index.html") {
		t.Error("a page of the app's own origin was refused")
	}
	for _, foreign := range []string{"other://x/nav.html", "https://evil.example/", "scorix://app2/", "data:text/html,x", "about:blank", ""} {
		if g.Allows(foreign) {
			t.Errorf("a document at %q got the bridge", foreign)
		}
	}
	g.Trust("https://login.example.com/start")
	if !g.Allows("https://login.example.com/cb") || g.Allows("scorix://app/") {
		t.Error("Trust did not move the origin Go itself navigated to")
	}
}

func TestGuardNeverTrustsDataURLs(t *testing.T) {
	var g Guard
	g.Trust("data:text/html,start")
	if g.Allows("data:text/html,attacker") {
		t.Error("one data: document vouched for every other data: document")
	}
}

func TestFirstRefusalWarnsOncePerOrigin(t *testing.T) {
	var g Guard
	if !g.FirstRefusal("https://evil.example/a") || g.FirstRefusal("https://evil.example/b") {
		t.Error("the same foreign origin should be reported once")
	}
	if !g.FirstRefusal("https://other.example/") {
		t.Error("a second foreign origin was not reported")
	}
}

func TestRefusalsRememberedAreBounded(t *testing.T) {
	var g Guard
	for i := 0; i < 10*maxRemembered; i++ {
		g.FirstRefusal("https://h" + strconv.Itoa(i) + ".example/")
	}
	if n := len(g.warned); n > maxRemembered {
		t.Fatalf("a page minting origins grew the warned set to %d entries", n)
	}
}

func TestGuardKeepsOutOnlyRemoteDocumentsOfAnotherOrigin(t *testing.T) {
	var g Guard
	g.Trust("scorix://app/index.html")
	for target, want := range map[string]bool{
		"https://evil.example/":      true,
		"http://127.0.0.1:8080/x":    true,
		"file:///etc/passwd":         true,
		"HTTPS://Evil.Example/":      true,
		"scorix://app/settings.html": false,
		"about:blank":                false,
		"about:srcdoc":               false,
		"blob:scorix://app/1f2e":     false,
		"data:text/html,x":           false,
		"other://x/page.html":        false,
		"::not a url":                true,
		"mailto:a@b.example":         true,
	} {
		if got := g.KeepsOut(target); got != want {
			t.Errorf("KeepsOut(%q) = %v, want %v", target, got, want)
		}
	}
	g.Trust("http://localhost:3000/")
	if g.KeepsOut("http://localhost:3000/page") || !g.KeepsOut("http://localhost:3001/") {
		t.Error("a dev server origin was not the trusted one")
	}
}

func TestFrameSourcesComeFromTheMostSpecificFrameDirective(t *testing.T) {
	for csp, want := range map[string]string{
		"default-src 'self'; frame-src 'self' https://www.youtube.com https://*.maps.example; img-src https://cdn.example": "https://www.youtube.com https://*.maps.example",
		"default-src 'self'; child-src https://pay.example":                                                                "https://pay.example",
		"default-src 'self' https://all.example":                                                                           "https://all.example",
		"Frame-Src HTTPS://A.Example:443; child-src https://ignored.example":                                               "HTTPS://A.Example:443",
		"frame-src https:": "https:",
		"frame-src https://first.example; frame-src https://second.example": "https://first.example",
		"frame-src 'none'; default-src https:":                              "",
		"":                                                                  "",
	} {
		if got := strings.Join(FrameSources(csp), " "); got != want {
			t.Errorf("FrameSources(%q) = %q, want %q", csp, got, want)
		}
	}
}

func TestAFrameSourceTheCSPAllowsIsNotKeptOut(t *testing.T) {
	var g Guard
	g.Trust("scorix://app/index.html")
	g.Frame(FrameSources("frame-src 'self' https://www.youtube.com https://*.maps.example HTTP://A.Example:8080 https://*.Upper.Example"))
	for target, want := range map[string]bool{
		"https://www.youtube.com/embed/x": false,
		"https://WWW.YouTube.com:443/x":   false,
		"http://www.youtube.com/embed/x":  true,
		"https://evil.youtube.com/":       true,
		"https://tiles.maps.example/1":    false,
		"https://a.b.maps.example/1":      false,
		"https://maps.example/":           true,
		"https://evilmaps.example/":       true,
		"http://a.example:8080/x":         false,
		"http://a.example/x":              true,
		"http://tiles.maps.example/1":     true,
		"https://x.upper.example/":        false,
	} {
		if got := g.KeepsOut(target); got != want {
			t.Errorf("KeepsOut(%q) = %v, want %v", target, got, want)
		}
	}
	g.Frame(FrameSources("frame-src https:"))
	if g.KeepsOut("https://anything.example/") || !g.KeepsOut("http://anything.example/") || !g.KeepsOut("mailto:x@y.example") {
		t.Error("a scheme-only source did not cover exactly its scheme")
	}
}

func TestABlobOfAnotherOriginIsKeptOutAndOneOfTheAppsIsNot(t *testing.T) {
	var g Guard
	g.Trust("https://app.scorix.localhost/")
	if !g.KeepsOut("blob:https://evil.example/u-1") {
		t.Error("a blob document of another origin may take the window")
	}
	if g.KeepsOut("blob:https://app.scorix.localhost/u-2") {
		t.Error("a blob document of the app's own origin was kept out")
	}
}

func TestEveryFormOfHostSourceTheCSPSpellsIsHonoured(t *testing.T) {
	var g Guard
	g.Trust("scorix://app/index.html")
	g.Frame(FrameSources("frame-src www.youtube.com *.maps.example https://pay.example:* http://legacy.example"))
	for target, want := range map[string]bool{
		"https://www.youtube.com/embed/x":   false,
		"http://www.youtube.com/embed/x":    false,
		"https://www.youtube.com:8443/x":    true,
		"https://tiles.maps.example/1":      false,
		"https://maps.example/1":            true,
		"https://pay.example:8443/checkout": false,
		"https://pay.example/checkout":      false,
		"http://pay.example/checkout":       true,
		"https://legacy.example/":           false,
		"http://legacy.example/":            false,
		"file://www.youtube.com/x":          true,
	} {
		if got := g.KeepsOut(target); got != want {
			t.Errorf("KeepsOut(%q) = %v, want %v", target, got, want)
		}
	}
}
