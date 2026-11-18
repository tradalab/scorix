package ioswebview

import "testing"

func TestTheAppsCustomSchemeIsItsOwnOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"scorix://app/index.html": "scorix://app",
		"SCORIX://App/a/b?x=1":    "scorix://app",
		"https://example.com/x":   "https://example.com",
		"about:blank":             "",
	} {
		if got := originOf(in); got != want {
			t.Errorf("originOf(%q) = %q, want %q", in, got, want)
		}
	}
	if got, ok := fromWebURL("scorix://app/a.txt"); !ok || got != "scorix://app/a.txt" {
		t.Errorf("fromWebURL = %q %v", got, ok)
	}
	if _, ok := fromWebURL("garbage"); ok {
		t.Error("a URL without a scheme was taken as the app's")
	}
}
