package app

import (
	"net/http"
	"testing"
)

// sameOrigin gates the /ipc WebSocket upgrade — a CSRF defense. Verify the
// matrix directly (the function is the security boundary for web mode).
func TestSameOrigin(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin (non-browser client)", "app.local", "", true},
		{"matching origin", "app.local", "http://app.local", true},
		{"matching origin https", "app.local:8080", "https://app.local:8080", true},
		{"case-insensitive host", "App.Local", "http://app.local", true},
		{"cross-site attacker", "app.local", "http://evil.example", false},
		{"port mismatch", "app.local:8080", "http://app.local:9090", false},
		{"unparseable origin", "app.local", "://not a url", false},
		{"subdomain is not same origin", "app.local", "http://x.app.local", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{Host: tc.host, Header: http.Header{}}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := sameOrigin(r); got != tc.want {
				t.Fatalf("sameOrigin(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}
