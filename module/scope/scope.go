// Package scope is what a module's manifest list grants: hosts, name globs or filesystem roots.
// Every kind reads the same way: an empty list grants nothing and "*" grants everything,
// and a refusal is a fault.CodeDenied naming the manifest key it fell outside.
package scope

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tradalab/scorix/fault"
)

const everything = "*"

type list struct {
	key     string
	all     bool
	entries []string
}

func newList(key string, raw []string, norm func(string) string) list {
	l := list{key: key}
	for _, e := range raw {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
		case e == everything:
			l.all = true
		default:
			l.entries = append(l.entries, norm(e))
		}
	}
	return l
}

func (l list) deny(kind, value string) error {
	return fault.Errorf(fault.CodeDenied, "%s %q is outside %s", kind, value, l.key).
		With("scope", l.key).With("value", value)
}

func (l list) String() string {
	switch {
	case l.all:
		return l.key + ": every value"
	case len(l.entries) == 0:
		return l.key + ": nothing"
	}
	return l.key + ": " + strings.Join(l.entries, ", ")
}

// Hosts matches a host exactly, with or without its port, and "*.example.com" matches every
// subdomain and example.com itself.
type Hosts struct{ list }

func NewHosts(key string, entries []string) Hosts {
	return Hosts{newList(key, entries, strings.ToLower)}
}

func (h Hosts) Allows(host string) bool {
	if h.all {
		return true
	}
	full := strings.ToLower(host)
	bare := full
	if hp, _, err := net.SplitHostPort(full); err == nil {
		bare = hp
	}
	for _, e := range h.entries {
		if strings.HasPrefix(e, "*.") {
			if strings.HasSuffix(bare, e[1:]) || bare == e[2:] {
				return true
			}
			continue
		}
		if full == e || bare == e {
			return true
		}
	}
	return false
}

func (h Hosts) Deny(host string) error { return h.deny("host", host) }

// Globs matches a whole string against patterns where "*" stands for any run of characters,
// "/" included: what it bounds are references like "<provider>/<model>", not paths.
type Globs struct{ list }

func NewGlobs(key string, patterns []string) Globs {
	return Globs{newList(key, patterns, func(s string) string { return s })}
}

func (g Globs) Allows(s string) bool {
	if g.all {
		return true
	}
	for _, p := range g.entries {
		if globMatch(p, s) {
			return true
		}
	}
	return false
}

func (g Globs) Deny(s string) error { return g.deny("value", s) }

func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// Paths admits a path under one of its roots, after resolving symlinks on both sides. A root
// is absolute or starts with $HOME, $DATA, $CACHE or $TEMP.
type Paths struct{ list }

func NewPaths(key string, roots []string, dataDir string) (Paths, error) {
	var expandErr error
	l := newList(key, roots, func(raw string) string {
		root, err := expandRoot(key, raw, dataDir)
		if err != nil && expandErr == nil {
			expandErr = err
		}
		return root
	})
	if expandErr != nil {
		return Paths{}, expandErr
	}
	return Paths{l}, nil
}

func expandRoot(key, raw, dataDir string) (string, error) {
	vars := map[string]func() (string, error){
		"$HOME":  os.UserHomeDir,
		"$CACHE": os.UserCacheDir,
		"$TEMP":  func() (string, error) { return os.TempDir(), nil },
		"$DATA": func() (string, error) {
			if dataDir == "" {
				return "", fmt.Errorf("the app has no data dir")
			}
			return dataDir, nil
		},
	}
	for name, resolve := range vars {
		if raw != name && !strings.HasPrefix(raw, name+"/") && !strings.HasPrefix(raw, name+string(filepath.Separator)) {
			continue
		}
		base, err := resolve()
		if err != nil {
			return "", fmt.Errorf("%s: %s: %w", key, name, err)
		}
		return filepath.Join(base, filepath.FromSlash(strings.TrimPrefix(raw, name))), nil
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s: %q must be absolute or start with $HOME, $DATA, $CACHE or $TEMP", key, raw)
	}
	return filepath.Clean(raw), nil
}

func canonical(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

func (p Paths) Admits(path string) error {
	if p.all {
		return nil
	}
	target := canonical(path)
	for _, root := range p.entries {
		rel, err := filepath.Rel(canonical(root), target)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	return p.deny("path", path)
}
