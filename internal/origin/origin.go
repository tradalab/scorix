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
	if scheme == "blob" && u.Opaque != "" {
		return Of(u.Opaque)
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
	framed  []string
	warned  map[string]bool
}

func FrameSources(csp string) []string {
	directives := map[string][]string{}
	for _, d := range strings.Split(csp, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		name := strings.ToLower(f[0])
		if _, seen := directives[name]; !seen {
			directives[name] = f[1:]
		}
	}
	for _, name := range []string{"frame-src", "child-src", "default-src"} {
		srcs, ok := directives[name]
		if !ok {
			continue
		}
		var out []string
		for _, src := range srcs {
			if !strings.HasPrefix(src, "'") {
				out = append(out, src)
			}
		}
		return out
	}
	return nil
}

func (g *Guard) Frame(sources []string) {
	g.mu.Lock()
	g.framed = sources
	g.mu.Unlock()
}

var defaultPorts = map[string]string{"http": "80", "https": "443"}

func framedBy(src string, u *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	src = strings.ToLower(src)
	if src == scheme+":" {
		return true
	}
	srcScheme, rest, explicit := strings.Cut(src, "://")
	if !explicit {
		if defaultPorts[scheme] == "" {
			return false
		}
		srcScheme, rest = scheme, src
	}
	if srcScheme != scheme && !(srcScheme == "http" && scheme == "https") {
		return false
	}
	hostport, _, _ := strings.Cut(rest, "/")
	host, port, hasPort := strings.Cut(hostport, ":")
	have := strings.ToLower(u.Hostname())
	if suffix, wild := strings.CutPrefix(host, "*."); wild {
		if !strings.HasSuffix(have, "."+suffix) {
			return false
		}
	} else if have != host {
		return false
	}
	target := u.Port()
	if target == "" {
		target = defaultPorts[scheme]
	}
	if !hasPort {
		return target == defaultPorts[scheme]
	}
	return port == "*" || port == target
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

var remote = map[string]bool{"http": true, "https": true, "file": true, "mailto": true, "blob": true}

func (g *Guard) KeepsOut(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return true
	}
	if !remote[strings.ToLower(u.Scheme)] || g.Allows(target) {
		return false
	}
	g.mu.Lock()
	framed := g.framed
	g.mu.Unlock()
	for _, src := range framed {
		if framedBy(src, u) {
			return false
		}
	}
	return true
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
