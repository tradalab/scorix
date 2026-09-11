package runner

import (
	"fmt"
	"sort"
	"strings"
)

type ShellKind struct {
	// Name is also the scaffold directory under static/scaffold.
	Name string
	// Page is relative to shell/. Empty means no demo page: the template is JSX.
	Page string
	// Directive heads files that use hooks. Empty for Vite, whose Rollup warns on "use client".
	Directive string
	Component string
	// React gates hooks/events.ts, which imports react.
	React bool
}

var shellKinds = map[string]ShellKind{
	"nextjs":     {Name: "nextjs", Page: "app/page.tsx", Directive: `"use client";`, Component: "Home", React: true},
	"vite-react": {Name: "vite-react", Page: "src/App.tsx", Component: "App", React: true},
	"vanilla-ts": {Name: "vanilla-ts"},
}

// Manifests without a shell block predate it and are all Next.js: keep this nextjs.
const DefaultShellKind = "nextjs"

func resolveShellKind(name string) (ShellKind, error) {
	if name == "" {
		name = DefaultShellKind
	}
	k, ok := shellKinds[name]
	if !ok {
		return ShellKind{}, fmt.Errorf("unknown shell %q, want one of %s", name, strings.Join(shellKindNames(), " | "))
	}
	return k, nil
}

func shellKindNames() []string {
	names := make([]string, 0, len(shellKinds))
	for n := range shellKinds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
