package origin

import (
	"net/url"
	"strings"
	"sync"
)

const (
	blank         = "about:blank"
	maxRemembered = 64
)

func Of(raw string) string {
	if raw == "" {
		return blank
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "about" {
		return blank
	}
	if u.Host == "" {
		return scheme + ":"
	}
	host := strings.ToLower(u.Host)
	if p := u.Port(); (scheme == "https" && p == "443") || (scheme == "http" && p == "80") {
		host = strings.TrimSuffix(host, ":"+p)
	}
	return scheme + "://" + host
}

type Guard struct {
	mu      sync.Mutex
	trusted string
	warned  map[string]bool
}

func (g *Guard) Trust(raw string) {
	g.mu.Lock()
	g.trusted = Of(raw)
	g.mu.Unlock()
}

func (g *Guard) Allows(docURL string) bool {
	o := Of(docURL)
	g.mu.Lock()
	defer g.mu.Unlock()
	return o != "" && o == g.trusted && o != "data:"
}

func (g *Guard) FirstRefusal(docURL string) bool {
	o := Of(docURL)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.warned == nil {
		g.warned = map[string]bool{}
	}
	if g.warned[o] || len(g.warned) >= maxRemembered {
		return false
	}
	g.warned[o] = true
	return true
}
