package llm

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// The headers a driver puts an API key in.
var keyHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

// KeySafeClient copies a client and drops the key headers on any redirect that
// leaves the host addressed or drops https. A driver only checks its base URL,
// which covers the first hop: Go carries Authorization across a same-domain
// redirect whatever the scheme, and strips nothing at all from a header it does
// not know, such as x-api-key.
//
// Stricter than Go, so a gateway that 307s to a regional twin answers 401. And
// CheckRedirect runs before the RoundTripper, so a client passed in as
// HTTPClient must not carry credentials of its own.
func KeySafeClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	next := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Hostname and not Host: a Location spelling out the default port is
		// the same machine, and dropping the key there answers 401.
		if req.URL.Hostname() != via[0].URL.Hostname() || (req.URL.Scheme != "https" && !IsLoopbackHost(req.URL.Hostname())) {
			for _, h := range keyHeaders {
				req.Header.Del(h)
			}
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("llm: stopped after %d redirects", len(via))
		}
		return nil
	}
	return &c
}

// This machine, not the LAN: a model on another box still means the data crossed
// the network, which is what a local-only slot exists to prevent.
func IsLoopbackHost(host string) bool {
	h := strings.TrimSuffix(host, ".")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// A neighbour rather than a hosted API: this machine, the LAN, a container, or a
// name only this network resolves. For the header wait and nothing else -
// Local() stays loopback-only, because LAN data still crossed a network.
func IsPrivateHost(host string) bool {
	if IsLoopbackHost(host) {
		return true
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	if !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".internal", ".lan", ".home", ".home.arpa", ".localhost", ".test"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// How long a server that accepted the connection may say nothing. Only for a
// hosted API: one on this machine or on the LAN may sit there loading a 70B
// model, which is why the flag is IsPrivateHost and not IsLoopbackHost. Not
// retried, see retry.go.
const headerWait = 90 * time.Second

func HeaderTimeout(base *http.Client, local bool) *http.Client {
	if local || base == nil {
		return base
	}
	c := *base
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		if c.Transport != nil {
			return base // someone else's transport; wrapping it would hide it
		}
		// A package variable tracing wrappers replace: an unchecked assertion
		// panics in New on a machine where everything else works.
		tr, ok = http.DefaultTransport.(*http.Transport)
		if !ok {
			return base
		}
	}
	t := tr.Clone()
	t.ResponseHeaderTimeout = headerWait
	c.Transport = t
	return &c
}
