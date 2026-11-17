package webview2

import "testing"

func TestEachSchemeHandlerAnswersOnlyItsOwnScheme(t *testing.T) {
	cases := []struct {
		scheme, uri string
		want        bool
	}{
		{"scorix", "scorix://app/index.html", true},
		{"scorix", "SCORIX://app/index.html", true},
		{"other", "scorix://app/index.html", false},
		{"scorix", "other://x/nav.html", false},
		{"scorix", "scorixx://app/", false},
		{"scorix", "scorix", false},
		{"scorix", "", false},
	}
	for _, c := range cases {
		if got := schemeOwns(c.scheme, c.uri); got != c.want {
			t.Errorf("schemeOwns(%q, %q) = %v, want %v", c.scheme, c.uri, got, c.want)
		}
	}
}
