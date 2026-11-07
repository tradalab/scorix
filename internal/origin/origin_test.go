package origin

import (
	"strconv"
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
