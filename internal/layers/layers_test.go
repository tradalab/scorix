package layers

import (
	"errors"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const root = "github.com/tradalab/scorix"

const (
	base = iota
	kernel
	library
	transport
	plugin
	host
	tool
)

var tierName = []string{"base", "kernel", "library", "transport", "plugin", "host", "tool"}

var tiers = map[string]int{
	"fault":              base,
	"logger":             base,
	"diag":               base,
	"scope":              base,
	"observe":            base,
	"internal/ze":        base,
	"internal/confvalue": base,

	"config": kernel,
	"conf":   kernel,
	"kernel": kernel,

	"fetch":                   library,
	"data":                    library,
	"data/page":               library,
	"data/page/pagetest":      library,
	"data/migratetest":        library,
	"redact":                  library,
	"oidc":                    library,
	"internal/redactpb":       library,
	"secrets":                 library,
	"proc":                    library,
	"netx":                    library,
	"llm/...":                 library,
	"menu":                    library,
	"webview":                 library,
	"window":                  library,
	"native":                  library,
	"internal/hostapi":        library,
	"internal/nativecall":     library,
	"internal/nativeclip":     library,
	"internal/ico":            library,
	"internal/origin":         library,
	"internal/singleinstance": library,
	"internal/deeplink":       library,
	"internal/desktopentry":   library,
	"internal/devctl":         library,
	"internal/mac":            library,

	"internal/ipc":     transport,
	"transport/...":    transport,
	"internal/mcpcore": transport,

	"module/...": plugin,

	"app":                 host,
	"server":              host,
	"server/servertest":   host,
	"worker":              host,
	"internal/driver/...": host,

	"cmd/...":               tool,
	"idl":                   tool,
	"idl/scorixpb":          library,
	"internal/cli/...":      tool,
	"internal/hostapi/gen":  tool,
	"internal/modulets/...": tool,
	"internal/e2e":          tool,
	"internal/layers":       tool,
	"mobile/...":            tool,
}

func tierOf(pkg string) (int, []string) {
	var rules []string
	tier := 0
	for rule, t := range tiers {
		match := rule == pkg
		if prefix, ok := strings.CutSuffix(rule, "/..."); ok {
			match = pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
		}
		if match {
			rules = append(rules, rule)
			tier = t
		}
	}
	sort.Strings(rules)
	return tier, rules
}

type graph map[string][]string

var targets = []string{"linux/amd64", "linux/arm64", "windows/amd64", "darwin/arm64", "android/arm64", "android/amd64"}

func load(t *testing.T) graph {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	g := graph{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if path != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, "go.mod")); path != dir && err == nil {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		for _, target := range targets {
			ctx := build.Default
			ctx.GOOS, ctx.GOARCH, _ = strings.Cut(target, "/")
			ctx.CgoEnabled = false
			p, err := ctx.ImportDir(path, 0)
			var noGo *build.NoGoError
			if errors.As(err, &noGo) {
				continue
			}
			if err != nil {
				return fmt.Errorf("%s on %s: %w", pkg, target, err)
			}
			if _, seen := g[pkg]; !seen {
				g[pkg] = nil
			}
			for _, imp := range p.Imports {
				if dep, ok := strings.CutPrefix(imp, root+"/"); ok {
					g[pkg] = append(g[pkg], dep)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestEveryPackageHasATier(t *testing.T) {
	for pkg := range load(t) {
		switch _, rules := tierOf(pkg); len(rules) {
		case 0:
			t.Errorf("%s has no tier in internal/layers", pkg)
		case 1:
		default:
			t.Errorf("%s matches %d tier rules %v; each package needs exactly one", pkg, len(rules), rules)
		}
	}
}

func TestEveryExactRuleNamesAPackage(t *testing.T) {
	g := load(t)
	for rule := range tiers {
		if strings.HasSuffix(rule, "/...") {
			continue
		}
		if _, ok := g[rule]; !ok {
			t.Errorf("tier rule %q names no package; it moved or was removed", rule)
		}
	}
}

func TestImportsPointDownOrSideways(t *testing.T) {
	for pkg, deps := range load(t) {
		from, rules := tierOf(pkg)
		if len(rules) != 1 {
			continue
		}
		for _, dep := range deps {
			to, rules := tierOf(dep)
			if len(rules) != 1 {
				continue
			}
			switch {
			case to == tool && from != tool:
				t.Errorf("%s (%s) imports the tool package %s", pkg, tierName[from], dep)
			case from != tool && to > from:
				t.Errorf("%s (%s) imports %s (%s), a higher tier", pkg, tierName[from], dep, tierName[to])
			}
		}
	}
}

var (
	gui   = []string{"window", "webview", "menu", "native", "app", "internal/driver"}
	cloud = []string{"server", "kernel", "conf", "observe", "data", "data/page", "redact", "transport/connect", "transport/rest", "transport/mcp", "transport/cli", "oidc", "worker"}
)

func TestTheCloudSidePullsInNoGUI(t *testing.T) {
	g := load(t)
	seen := map[string]bool{}
	var walk func(string)
	walk = func(pkg string) {
		if seen[pkg] {
			return
		}
		seen[pkg] = true
		for _, dep := range g[pkg] {
			walk(dep)
		}
	}
	for _, root := range cloud {
		if _, ok := g[root]; !ok {
			t.Errorf("cloud package %q does not exist", root)
		}
		clear(seen)
		walk(root)
		for pkg := range seen {
			for _, ui := range gui {
				if pkg == ui || strings.HasPrefix(pkg, ui+"/") {
					t.Errorf("%s depends on %s, which only a UI host may carry", root, pkg)
				}
			}
		}
	}
}
