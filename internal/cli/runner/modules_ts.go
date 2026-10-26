package runner

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/internal/cli/template"
)

const modulePrefix = "github.com/tradalab/scorix/module/"

func moduleClientFile(root string, cfg *ProjectConfig, check bool) (*generatedFile, error) {
	out := filepath.Join(root, "shell", "api", "modules.ts")
	// Missing is drift to --check and apps never had this file; same rule as docs/ipc-surface.md.
	if check && !fileExists(out) {
		return nil, nil
	}
	dirs, err := appModules(root, cfg)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	var fragments []string
	for _, d := range names {
		// llm and template have no fragment: see modulets.Modules.
		if src, err := template.ReadFile(template.ShellModuleDir + "/" + d + ".ts"); err == nil {
			fragments = append(fragments, src)
		}
	}
	if len(fragments) == 0 && !fileExists(out) {
		return nil, nil
	}
	return &generatedFile{Path: out, Template: mustRead(template.ShellModules), Data: map[string]any{"Fragments": fragments}, Force: true}, nil
}

// appModules names the module/ packages the app uses: turned on in scorix.yaml or registered in Go.
func appModules(root string, cfg *ProjectConfig) (map[string]bool, error) {
	dirs := map[string]bool{}
	for _, m := range enabledBuiltins(cfg) {
		dirs[path.Base(m.Import)] = true
	}
	imported, err := registeredModules(root)
	if err != nil {
		return nil, err
	}
	for _, d := range imported {
		dirs[d] = true
	}
	// generate model registers sqlx and may run later; the output must not depend on order.
	if hasTables(root) {
		dirs["sqlx"] = true
	}
	return dirs, nil
}

// Not the app's Go, or too big to walk.
var skipDirs = map[string]bool{"shell": true, "node_modules": true, "android": true, "vendor": true}

// An import alone does not count: model code imports sqlx without putting it on IPC.
func registeredModules(root string) ([]string, error) {
	seen := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// Imports first: a full parse of every file would slow each generate.
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			return nil // go build reports it
		}
		// It may be the previous run's. Old scaffolds lack "DO NOT EDIT", so they stay the app's.
		if ast.IsGenerated(f) {
			return nil
		}
		pkgOf := map[string]string{}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			rest, ok := strings.CutPrefix(ip, modulePrefix)
			if !ok || strings.Contains(rest, "/") {
				continue
			}
			alias := rest
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			pkgOf[alias] = rest
		}
		if len(pkgOf) == 0 {
			return nil
		}
		if f, err = parser.ParseFile(fset, p, nil, 0); err != nil {
			return nil
		}
		// The model scaffold registers sqlx through a variable.
		varOf := map[string]string{}
		ast.Inspect(f, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						if pkg := modulePkgOf(as.Rhs[i], pkgOf); pkg != "" {
							varOf[id.Name] = pkg
						}
					}
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || (sel.Sel.Name != "Module" && sel.Sel.Name != "DefaultModule") {
				return true
			}
			if pkg := modulePkgOf(call.Args[0], pkgOf); pkg != "" {
				seen[pkg] = true
			} else if id, ok := call.Args[0].(*ast.Ident); ok && varOf[id.Name] != "" {
				seen[varOf[id.Name]] = true
			}
			return true
		})
		return nil
	})
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	return out, err
}

// modulePkgOf names the module package e builds: pkg.New(...), &pkg.T{} or pkg.T{}.
func modulePkgOf(e ast.Expr, pkgOf map[string]string) string {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	var fn ast.Expr
	switch e := e.(type) {
	case *ast.CallExpr:
		fn = e.Fun
	case *ast.CompositeLit:
		fn = e.Type
	}
	if sel, ok := fn.(*ast.SelectorExpr); ok {
		if x, ok := sel.X.(*ast.Ident); ok {
			return pkgOf[x.Name]
		}
	}
	return ""
}
