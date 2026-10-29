// Package modulets writes the TypeScript client of each built-in module, one fragment per
// package under internal/cli/template/static/codegen/shell/modules. `scorix generate proto` puts
// together the fragments an app uses; this package is not linked into the CLI.
package modulets

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tradalab/scorix/module/biometric"
	"github.com/tradalab/scorix/module/browser"
	"github.com/tradalab/scorix/module/clipboard"
	"github.com/tradalab/scorix/module/dialog"
	"github.com/tradalab/scorix/module/env"
	"github.com/tradalab/scorix/module/fs"
	"github.com/tradalab/scorix/module/httpfetch"
	"github.com/tradalab/scorix/module/logfwd"
	"github.com/tradalab/scorix/module/notification"
	"github.com/tradalab/scorix/module/scanner"
	"github.com/tradalab/scorix/module/share"
	"github.com/tradalab/scorix/module/sqlx"
	"github.com/tradalab/scorix/module/store"
	"github.com/tradalab/scorix/module/systemtray"
	"github.com/tradalab/scorix/module/updater"
	"github.com/tradalab/scorix/module/widget"
)

// Dir is the package's directory under module/; Name is what mod:<name>: carries. llm is left
// out: it registers its handlers in a loop, with a stream among them.
var Modules = []struct {
	Dir, Name string
	Type      reflect.Type
}{
	{"biometric", "biometric", reflect.TypeFor[*biometric.Module]()},
	{"browser", "browser", reflect.TypeFor[*browser.BrowserModule]()},
	{"clipboard", "clipboard", reflect.TypeFor[*clipboard.ClipboardModule]()},
	{"dialog", "dialog", reflect.TypeFor[*dialog.DialogModule]()},
	{"env", "env", reflect.TypeFor[*env.EnvModule]()},
	{"fs", "fs", reflect.TypeFor[*fs.FSModule]()},
	{"httpfetch", "http", reflect.TypeFor[*httpfetch.HTTPModule]()},
	{"logfwd", "log", reflect.TypeFor[*logfwd.LogModule]()},
	{"notification", "notification", reflect.TypeFor[*notification.NotificationModule]()},
	{"scanner", "scanner", reflect.TypeFor[*scanner.Module]()},
	{"share", "share", reflect.TypeFor[*share.Module]()},
	{"sqlx", "sqlx", reflect.TypeFor[*sqlx.Module]()},
	{"store", "store", reflect.TypeFor[*store.StoreModule]()},
	{"systemtray", "systemtray", reflect.TypeFor[*systemtray.SystemTrayModule]()},
	{"updater", "updater", reflect.TypeFor[*updater.UpdaterModule]()},
	{"widget", "widget", reflect.TypeFor[*widget.Module]()},
}

// Generate rewrites, or with check only compares, every fragment for the scorix checkout at root.
func Generate(root string, check bool) error {
	out := filepath.Join(root, "internal", "cli", "template", "static", "codegen", "shell", "modules")
	for _, m := range Modules {
		src, err := Fragment(filepath.Join(root, "module", m.Dir), m.Name, m.Type)
		if err != nil {
			return fmt.Errorf("module/%s: %w", m.Dir, err)
		}
		path := filepath.Join(out, m.Dir+".ts")
		disk, err := os.ReadFile(path)
		// A Windows checkout with core.autocrlf turns the fragment to CRLF; that is not drift.
		if err == nil && strings.ReplaceAll(string(disk), "\r\n", "\n") == src {
			continue
		}
		if check {
			return fmt.Errorf("%s does not match module/%s; run go generate ./internal/modulets", path, m.Dir)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			return err
		}
		fmt.Printf("==> wrote %s\n", path)
	}
	return nil
}

type method struct {
	name string
	doc  string
}

// exposed reads the methods a module hands to module.Expose, in the order it does.
func exposed(dir string) ([]method, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	fset := token.NewFileSet()
	docs := map[string]string{}
	var names []string
	// Build tags are ignored, so two platform files may expose the same name.
	seen := map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Recv != nil && n.Doc != nil {
					docs[n.Name.Name] = n.Doc.Text()
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Expose" || len(n.Args) < 2 {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "module" {
					return true
				}
				lit, ok := n.Args[1].(*ast.BasicLit)
				if !ok {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil && !seen[s] {
					seen[s] = true
					names = append(names, s)
				}
			}
			return true
		})
	}
	out := make([]method, len(names))
	for i, n := range names {
		out[i] = method{name: n, doc: docs[n]}
	}
	return out, nil
}

var (
	ctxType     = reflect.TypeFor[context.Context]()
	timeType    = reflect.TypeFor[time.Time]()
	rawType     = reflect.TypeFor[json.RawMessage]()
	marshalType = reflect.TypeFor[json.Marshaler]()
)

type writer struct {
	prefix string
	// named holds each struct type's TS name; input marks those a page sends.
	named map[reflect.Type]string
	input map[reflect.Type]bool
	order []reflect.Type
	taken map[string]reflect.Type
}

// Fragment is the TS for one module: an interface per struct its methods take or return, and a
// const of calls named after the module.
func Fragment(dir, name string, t reflect.Type) (string, error) {
	methods, err := exposed(dir)
	if err != nil {
		return "", err
	}
	if len(methods) == 0 {
		return "", fmt.Errorf("no module.Expose call found")
	}
	w := &writer{prefix: exported(name), named: map[reflect.Type]string{}, input: map[reflect.Type]bool{}, taken: map[string]reflect.Type{}}
	var calls strings.Builder
	for _, m := range methods {
		mm, ok := t.MethodByName(m.name)
		if !ok {
			return "", fmt.Errorf("module.Expose names %s, which %s does not have", m.name, t)
		}
		// In is the receiver first; Expose takes an optional context and an optional argument.
		var arg reflect.Type
		for i := 1; i < mm.Type.NumIn(); i++ {
			if in := mm.Type.In(i); in != ctxType {
				arg = in
			}
		}
		param := ""
		if arg != nil && !(arg.Kind() == reflect.Struct && arg.NumField() == 0) {
			ts, err := w.ts(arg, true)
			if err != nil {
				return "", fmt.Errorf("%s: %w", m.name, err)
			}
			// Its fields are all optional (see ts), so it may be left out too.
			if arg.Kind() == reflect.Struct || arg.Kind() == reflect.Pointer && arg.Elem().Kind() == reflect.Struct {
				param = "params?: " + ts
			} else {
				param = "params: " + ts
			}
		}
		res, err := w.ts(mm.Type.Out(0), false)
		if err != nil {
			return "", fmt.Errorf("%s: %w", m.name, err)
		}
		if mm.Type.Out(0).Kind() == reflect.Pointer {
			res += " | null"
		}
		if doc := jsdoc(m.doc); doc != "" {
			calls.WriteString("  /** " + doc + " */\n")
		}
		cmd := strconv.Quote("mod:" + name + ":" + m.name)
		if param == "" {
			fmt.Fprintf(&calls, "  %s: () => scorix.invoke<%s>(%s),\n", lowerFirst(m.name), res, cmd)
		} else {
			fmt.Fprintf(&calls, "  %s: (%s) => scorix.invoke<%s>(%s, params),\n", lowerFirst(m.name), param, res, cmd)
		}
	}
	var b strings.Builder
	for _, st := range w.order {
		fmt.Fprintf(&b, "\nexport interface %s {\n", w.named[st])
		if err := w.fields(&b, st, w.input[st]); err != nil {
			return "", err
		}
		b.WriteString("}\n")
	}
	fmt.Fprintf(&b, "\nexport const %s = {\n%s};\n", lowerFirst(name), calls.String())
	return b.String(), nil
}

// ts names t in TypeScript. A struct a page sends has every field optional: the module decodes
// with encoding/json, so a field left out arrives as its zero value.
func (w *writer) ts(t reflect.Type, input bool) (string, error) {
	switch {
	case t == timeType:
		return "string", nil
	case t == rawType:
		return "unknown", nil
	case t.Implements(marshalType) || reflect.PointerTo(t).Implements(marshalType):
		return "unknown", nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return w.ts(t.Elem(), input)
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Interface:
		return "unknown", nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string", nil // encoding/json sends []byte as base64
		}
		e, err := w.ts(t.Elem(), input)
		if err != nil {
			return "", err
		}
		if strings.Contains(e, " | ") {
			e = "(" + e + ")"
		}
		return e + "[]", nil
	case reflect.Map:
		v, err := w.ts(t.Elem(), input)
		if err != nil {
			return "", err
		}
		return "Record<string, " + v + ">", nil
	case reflect.Struct:
		if t.Name() == "" {
			return "", fmt.Errorf("anonymous struct %s: give it a name to type it in TS", t)
		}
		if n, ok := w.named[t]; ok {
			// Seen as a reply first: what it holds is sent too now.
			if input && !w.input[t] {
				w.input[t] = true
				if err := w.fields(nil, t, true); err != nil {
					return "", err
				}
			}
			return n, nil
		}
		w.input[t] = input
		n := t.Name()
		if !strings.HasPrefix(n, w.prefix) {
			n = w.prefix + n
		}
		if prev, ok := w.taken[n]; ok {
			return "", fmt.Errorf("%s and %s would both be %s in TS", prev, t, n)
		}
		w.named[t], w.taken[n] = n, t
		w.order = append(w.order, t)
		// Fields are named now so their types land in order; fields() writes them later.
		if err := w.fields(nil, t, input); err != nil {
			return "", err
		}
		return n, nil
	}
	return "", fmt.Errorf("no TS type for %s", t)
}

// fields writes t's JSON fields to b, or with b nil only names the types they use.
func (w *writer) fields(b *strings.Builder, t reflect.Type, input bool) error {
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		key, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && key == "" && f.Type.Kind() == reflect.Struct {
			if err := w.fields(b, f.Type, input); err != nil {
				return err
			}
			continue
		}
		if key == "" {
			key = f.Name
		}
		ts, err := w.ts(f.Type, input)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		if b == nil {
			continue
		}
		opt := ""
		if input || strings.Contains(opts, "omitempty") {
			opt = "?"
		}
		if f.Type.Kind() == reflect.Pointer && !input {
			ts += " | null"
		}
		fmt.Fprintf(b, "  %s%s: %s;\n", key, opt, ts)
	}
	return nil
}

// jsdoc keeps a Go doc comment's prose, up to the scorix.invoke example the client replaces.
func jsdoc(doc string) string {
	var keep []string
	for line := range strings.SplitSeq(strings.TrimSpace(doc), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "scorix.invoke(") {
			break
		}
		keep = append(keep, line)
	}
	return strings.ReplaceAll(strings.Join(keep, " "), "*/", "* /")
}

func exported(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
