package httpfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/module/moduletest"
)

func newTestModule(t *testing.T, allow ...string) (*HTTPModule, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"echo":%q}`, r.Header.Get("X-Probe"))
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0x00, 0x01, 0xFF})
		case "/redirect-out":
			http.Redirect(w, r, "http://evil.invalid/x", http.StatusFound)
		}
	}))
	t.Cleanup(ts.Close)

	m := New()
	u, _ := url.Parse(ts.URL)
	for i, a := range allow {
		if a == "SELF" {
			allow[i] = u.Host
		}
	}
	hosts := make([]any, len(allow))
	for i, a := range allow {
		hosts[i] = a
	}
	moduletest.Load(t, m, map[string]any{"allow": hosts})
	return m, ts
}

func TestFetchAllowedHost(t *testing.T) {
	m, ts := newTestModule(t, "SELF")
	resp, err := m.Fetch(context.Background(), FetchRequest{
		URL: ts.URL + "/json", Headers: map[string]string{"X-Probe": "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || resp.Base64 || !strings.Contains(resp.Body, `"hi"`) {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestFetchBinaryBase64(t *testing.T) {
	m, ts := newTestModule(t, "SELF")
	resp, err := m.Fetch(context.Background(), FetchRequest{URL: ts.URL + "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Base64 || resp.Body != "AAH/" {
		t.Fatalf("binary resp = %+v", resp)
	}
}

func TestFetchDeniedHost(t *testing.T) {
	m, ts := newTestModule(t) // empty allowlist
	_, err := m.Fetch(context.Background(), FetchRequest{URL: ts.URL + "/json"})
	if fault.CodeOf(err) != fault.CodeDenied {
		t.Fatalf("err = %v, want denied", err)
	}
}

func TestFetchRedirectOutsideAllowlistBlocked(t *testing.T) {
	m, ts := newTestModule(t, "SELF")
	_, err := m.Fetch(context.Background(), FetchRequest{URL: ts.URL + "/redirect-out"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want redirect denial", err)
	}
}

func TestAStarAllowsEveryHostLikeEveryOtherScope(t *testing.T) {
	m, ts := newTestModule(t, "*")
	if _, err := m.Fetch(context.Background(), FetchRequest{URL: ts.URL + "/json"}); err != nil {
		t.Fatalf(`modules.http.allow: ["*"] refused a host: %v`, err)
	}
}
