package llm

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// A transport that answers every request with a redirect to `to`, once, then
// 200. Real servers are all on loopback here, and loopback is the one host the
// guard lets keep a key over plain http - so a test built on httptest cannot
// reach the scheme half of the rule at all.
type redirectOnce struct {
	to   string
	seen []*http.Request
}

func (r *redirectOnce) RoundTrip(req *http.Request) (*http.Response, error) {
	r.seen = append(r.seen, req)
	rec := httptest.NewRecorder()
	if len(r.seen) == 1 {
		rec.Header().Set("Location", r.to)
		rec.WriteHeader(http.StatusTemporaryRedirect)
	} else {
		rec.WriteHeader(http.StatusOK)
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func TestAKeyFollowsARedirectOnlyWhereItIsStillSafe(t *testing.T) {
	for _, tc := range []struct {
		name, from, to string
		keep           bool
	}{
		{"https to http on the same host", "https://gw.example.com/v1", "http://gw.example.com/v1", false},
		{"another host on the same domain", "https://gw.example.com/v1", "https://gw-eu.example.com/v1", false},
		{"the same host with the port spelled out", "https://gw.example.com/v1", "https://gw.example.com:443/v1", true},
		{"a deeper path on the same host", "https://gw.example.com/v1", "https://gw.example.com/v2", true},
		{"loopback over plain http", "http://127.0.0.1:1234/v1", "http://127.0.0.1:1234/v2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &redirectOnce{to: tc.to}
			c := KeySafeClient(&http.Client{Transport: rt})
			req, err := http.NewRequest(http.MethodGet, tc.from, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer sk-secret")
			req.Header.Set("X-Api-Key", "sk-ant-secret")
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if len(rt.seen) != 2 {
				t.Fatalf("hops = %d", len(rt.seen))
			}
			last := rt.seen[1].Header
			got := last.Get("Authorization") != "" || last.Get("X-Api-Key") != ""
			if got != tc.keep {
				t.Errorf("second hop %s kept the key = %v, want %v", tc.to, got, tc.keep)
			}
		})
	}
}

// Go builds each redirect from the request the caller made, so a header
// dropped on one hop is back on the next. The rule has to be judged against
// the host the caller addressed, not the hop before: a detour through
// somewhere else must not carry the key, and coming back must not be the way
// to lose it.
func TestAKeyDoesNotComeBackAfterLeaving(t *testing.T) {
	var seen []*http.Request
	c := KeySafeClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req)
		rec := httptest.NewRecorder()
		switch len(seen) {
		case 1:
			rec.Header().Set("Location", "https://middle.example.com/v1")
			rec.WriteHeader(http.StatusTemporaryRedirect)
		case 2:
			rec.Header().Set("Location", "https://gw.example.com/v1")
			rec.WriteHeader(http.StatusTemporaryRedirect)
		default:
			rec.WriteHeader(http.StatusOK)
		}
		resp := rec.Result()
		resp.Request = req
		return resp, nil
	})})
	req, _ := http.NewRequest(http.MethodGet, "https://gw.example.com/v1", nil)
	req.Header.Set("X-Api-Key", "sk-ant-secret")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(seen) != 3 {
		t.Fatalf("hops = %d", len(seen))
	}
	if got := seen[1].Header.Get("X-Api-Key"); got != "" {
		t.Errorf("the detour through %s carried the key: %q", seen[1].URL, got)
	}
	// Back on the host the caller addressed, which is the only one that ever
	// had it; the detour did not.
	if got := seen[2].Header.Get("X-Api-Key"); got == "" {
		t.Errorf("the key was lost coming back to %s", seen[2].URL)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKeySafeClientLeavesTheCallersClientAlone(t *testing.T) {
	base := &http.Client{}
	c := KeySafeClient(base)
	if base.CheckRedirect != nil {
		t.Error("the caller's own client was changed")
	}
	if c == base {
		t.Error("the caller's client was handed back")
	}
}

// HeaderTimeout clones the transport, so the client it returns has a connection
// pool of its own - that is what makes closing it both possible and necessary.
// Counted in sockets the server saw, because "it has a transport" says nothing
// about whether anything is held.
func TestAClonedTransportHasItsOwnPoolAndGivesItBack(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			mu.Lock()
			conns++
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()

	get := func(c *http.Client) {
		t.Helper()
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		// Read to the end, or the connection never goes back in the pool and
		// this measures the wrong thing.
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return conns
	}

	// local=false, although the server is on loopback: the flag is the caller's
	// decision, and testing the mechanism needs the branch that clones.
	a := HeaderTimeout(&http.Client{}, false)
	get(a)
	get(a)
	if n := count(); n != 1 {
		t.Fatalf("%d sockets for two requests on one client; the pool is not being reused", n)
	}

	b := HeaderTimeout(&http.Client{}, false)
	get(b)
	if n := count(); n != 2 {
		t.Errorf("%d sockets after a second client, want 2: the two share a pool", n)
	}

	a.Transport.(*http.Transport).CloseIdleConnections()
	get(a)
	if n := count(); n != 3 {
		t.Errorf("%d sockets after closing the idle ones, want 3: they were not given back", n)
	}
}
