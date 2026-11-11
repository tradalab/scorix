package androidwebview

import (
	"testing"
	"time"
)

func newExits(home string) (*exits, chan string) {
	opened := make(chan string, 4)
	e := &exits{external: func(url string) { opened <- url }}
	e.guard.Trust(home)
	return e, opened
}

func openedWithin(opened chan string) string {
	select {
	case url := <-opened:
		return url
	case <-time.After(time.Second):
		return ""
	}
}

func TestTheAppsOwnOriginStaysInTheWebView(t *testing.T) {
	e, opened := newExits("https://app.scorix.localhost/index.html")
	if e.keepsOut("https://app.scorix.localhost/other.html", true) {
		t.Fatal("a page of the app's own origin was kept out")
	}
	if url := openedWithin(opened); url != "" {
		t.Fatalf("a page of the app was handed out: %s", url)
	}
}

func TestAClickedLinkToAnotherOriginOpensOutside(t *testing.T) {
	e, opened := newExits("https://app.scorix.localhost/")
	for _, target := range []string{"https://example.com/x", "mailto:a@b.example"} {
		if !e.keepsOut(target, true) {
			t.Fatalf("%s stayed in the WebView", target)
		}
		if got := openedWithin(opened); got != target {
			t.Fatalf("%s was handed out as %q", target, got)
		}
	}
}

func TestAScriptedNavigationOutIsKeptOutButNotOpened(t *testing.T) {
	e, opened := newExits("https://app.scorix.localhost/")
	if !e.keepsOut("https://example.com/x", false) {
		t.Fatal("a scripted navigation to another origin stayed in the WebView")
	}
	if url := openedWithin(opened); url != "" {
		t.Fatalf("a navigation no one clicked was handed out: %s", url)
	}
}

func TestADevServerTheAppNavigatedToIsItsOrigin(t *testing.T) {
	e, _ := newExits("https://app.scorix.localhost/")
	e.guard.Trust("http://10.0.2.2:5173/")
	if e.keepsOut("http://10.0.2.2:5173/page", false) {
		t.Fatal("the dev server the app navigated to was kept out")
	}
	if !e.keepsOut("https://app.scorix.localhost/", false) {
		t.Fatal("the origin before the navigation is still trusted")
	}
}

func TestWithoutAnOpenerAClickIsOnlyKeptOut(t *testing.T) {
	e := &exits{}
	e.guard.Trust("https://app.scorix.localhost/")
	if !e.keepsOut("https://example.com/", true) {
		t.Fatal("kept nothing out without an opener")
	}
	time.Sleep(100 * time.Millisecond)
}

func TestASchemeOnlyAnotherAppHandlesNeverReplacesThePage(t *testing.T) {
	e, opened := newExits("https://app.scorix.localhost/")
	for _, target := range []string{"tel:+84123", "intent://scan/#Intent;scheme=zxing;end", "market://details?id=x", "geo:0,0", "sms:123"} {
		if !e.keepsOut(target, true) {
			t.Errorf("%s replaced the app's page", target)
			continue
		}
		if got := openedWithin(opened); got != target {
			t.Errorf("%s was handed out as %q", target, got)
		}
	}
}

func TestDocumentsWithoutAnOriginOfTheirOwnStay(t *testing.T) {
	e, _ := newExits("https://app.scorix.localhost/")
	for _, target := range []string{"about:blank", "javascript:void(0)", "data:text/html,hi"} {
		if e.keepsOut(target, false) {
			t.Errorf("%s was kept out", target)
		}
	}
}
