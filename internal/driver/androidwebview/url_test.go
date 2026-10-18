package androidwebview

import "testing"

func TestWebURLRoundTrip(t *testing.T) {
	cases := []struct{ app, web string }{
		{"scorix://app/index.html", "https://app.scorix.localhost/index.html"},
		{"scorix://app/a/b.js?v=1#top", "https://app.scorix.localhost/a/b.js?v=1#top"},
		{"assets:///logo.png", "https://assets.localhost/logo.png"},
		{"my-app://ui/", "https://ui.my-app.localhost/"},
	}
	for _, c := range cases {
		if got := toWebURL(c.app); got != c.web {
			t.Errorf("toWebURL(%q) = %q, want %q", c.app, got, c.web)
		}
		got, ok := fromWebURL(c.web)
		if !ok || got != c.app {
			t.Errorf("fromWebURL(%q) = %q, %v, want %q", c.web, got, ok, c.app)
		}
	}
}

func TestWebURLPassThrough(t *testing.T) {
	for _, raw := range []string{"http://10.0.2.2:5173/", "https://example.com/x", "about:blank", "data:text/html,hi"} {
		if got := toWebURL(raw); got != raw {
			t.Errorf("toWebURL(%q) = %q, want it unchanged", raw, got)
		}
	}
	for _, raw := range []string{"https://example.com/x", "http://app.scorix.localhost/", "https://app.http.localhost/", "https://a.b_c.localhost/"} {
		if got, ok := fromWebURL(raw); ok {
			t.Errorf("fromWebURL(%q) = %q, want no app URL", raw, got)
		}
	}
}

func TestOriginOf(t *testing.T) {
	if got := originOf("https://app.scorix.localhost/index.html?x=1"); got != "https://app.scorix.localhost" {
		t.Errorf("originOf = %q", got)
	}
	if got := originOf("http://10.0.2.2:5173/"); got != "http://10.0.2.2:5173" {
		t.Errorf("originOf dev = %q", got)
	}
}
