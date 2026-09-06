package notification

import "testing"

func TestActivationRoundTrip(t *testing.T) {
	cases := []struct{ id, action string }{
		{"build-1", DefaultAction},
		{"build-1", "open"},
		{"id with spaces & =", "retry now"},
	}
	for _, c := range cases {
		raw := activationURL("myapp", c.id, c.action)
		id, action, ok := parseActivation(raw)
		if !ok || id != c.id || action != c.action {
			t.Fatalf("%q -> id=%q action=%q ok=%v, want %q/%q", raw, id, action, ok, c.id, c.action)
		}
	}
}

func TestActivationNeedsAnIDAndAScheme(t *testing.T) {
	if got := activationURL("myapp", "", "open"); got != "" {
		t.Fatalf("an idless notification has nowhere to call back: %q", got)
	}
	if got := activationURL("", "x", "open"); got != "" {
		t.Fatalf("without a declared scheme there is no callback URL: %q", got)
	}
}

// An app's own deep links share the scheme, so anything that is not ours has to
// pass through untouched instead of being swallowed as a notification click.
func TestParseActivationIgnoresOtherDeepLinks(t *testing.T) {
	for _, raw := range []string{
		"myapp://profile/42",
		"myapp://notify",           // no id
		"myapp://notify?action=go", // no id
		"not a url at all",
		"",
		"/plain/path",
	} {
		if _, _, ok := parseActivation(raw); ok {
			t.Fatalf("%q was claimed as a notification activation", raw)
		}
	}
}

// Windows hands back "scheme:notify?..." (no authority) in some launch paths.
func TestParseActivationAcceptsAuthoritylessForm(t *testing.T) {
	id, action, ok := parseActivation("myapp:notify?id=b7&action=open")
	if !ok || id != "b7" || action != "open" {
		t.Fatalf("id=%q action=%q ok=%v", id, action, ok)
	}
}

func TestDeliverReachesSubscribersAndTheFrontend(t *testing.T) {
	subMu.Lock()
	subs, emit = nil, nil
	subMu.Unlock()
	t.Cleanup(func() {
		subMu.Lock()
		subs, emit = nil, nil
		subMu.Unlock()
	})

	var gotID, gotAction string
	OnActivate(func(id, action string) { gotID, gotAction = id, action })
	var emitted int
	subMu.Lock()
	emit = func(string, string) { emitted++ }
	subMu.Unlock()

	deliver("b7", "open")
	if gotID != "b7" || gotAction != "open" || emitted != 1 {
		t.Fatalf("go=%q/%q frontend=%d", gotID, gotAction, emitted)
	}
}

// A real deep link can share the "notify" host with a path after it; claiming
// that as an activation would swallow the app's own navigation.
func TestParseActivationRejectsNotifyWithAPath(t *testing.T) {
	if _, _, ok := parseActivation("myapp://notify/thread/9?id=5"); ok {
		t.Fatal("a deep link under notify/ was claimed as a notification click")
	}
}

// The two backends need different halves of the app's identity: Windows matches
// the AUMID, Linux shows the name. Falling back to the wrong one puts
// "com.example.app" in front of the user.
func TestAppInfoPicksTheRightHalf(t *testing.T) {
	full := appInfo{ID: "com.example.app", Name: "Example"}
	if full.aumid() != "com.example.app" || full.display() != "Example" {
		t.Fatalf("aumid=%q display=%q", full.aumid(), full.display())
	}
	if got := (appInfo{Name: "Example"}).aumid(); got != "Example" {
		t.Fatalf("no identifier: aumid = %q", got)
	}
	if got := (appInfo{ID: "com.example.app"}).display(); got != "com.example.app" {
		t.Fatalf("no name: display = %q", got)
	}
}
