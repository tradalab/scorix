// Package conf loads a network service's YAML config strictly, so a mistyped key, a missing
// one or a ${VAR} stops the service at start instead of running on a silent default.
package conf

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tradalab/scorix/internal/confvalue"
)

// Load reads the YAML at path into dst, a pointer to a struct, honoring go-zero's json tag options.
// It fails on a key no field takes, on a ${VAR}, on a required field left unset (null or empty env
// counts as unset). An env var set for an optional block the file leaves out is logged and ignored.
func Load(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("conf: %w", err)
	}
	return loadService(path, data, dst, os.LookupEnv, os.ReadFile)
}

type serviceLoader struct {
	file     string
	lookup   func(string) (string, bool)
	readFile func(string) ([]byte, error)
	missing  []string
	errs     []error
	read     map[string]bool
	unread   [][2]string
}

func loadService(file string, data []byte, dst any, lookup func(string) (string, bool), readFile func(string) ([]byte, error)) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("conf: Load needs a pointer to a struct, got %T", dst)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("conf: %s: %w", file, err)
	}
	var root *yaml.Node
	if len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	l := &serviceLoader{file: file, lookup: lookup, readFile: readFile, read: map[string]bool{}}
	l.object("", root, rv.Elem())
	for _, u := range l.unread {
		if !l.read[u[0]] {
			slog.Warn("conf: an env var of an optional block the file leaves out is ignored; add the block to turn it on", "env", u[0], "block", u[1], "file", file)
		}
	}
	if len(l.missing) > 0 {
		l.errs = append(l.errs, fmt.Errorf("conf: %s leaves required keys unset: %s", file, strings.Join(l.missing, ", ")))
	}
	return errors.Join(l.errs...)
}

type fieldSpec struct {
	key      string
	inline   bool
	optional bool
	def      string
	hasDef   bool
	env      string
	secret   bool
	fromFile bool
}

func (l *serviceLoader) spec(path string, f reflect.StructField) (fieldSpec, bool) {
	s := fieldSpec{key: f.Name}
	yamlName, yamlOpts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	jsonName, jsonOpts, _ := strings.Cut(f.Tag.Get("json"), ",")
	if yamlName == "-" || jsonName == "-" {
		return s, false
	}
	switch {
	case yamlName != "":
		s.key = yamlName
	case jsonName != "":
		s.key = jsonName
	}
	s.inline = yamlOpts == "inline" || (f.Anonymous && yamlName == "" && jsonName == "" && f.Type.Kind() == reflect.Struct)
	for _, opts := range []string{f.Tag.Get("conf"), jsonOpts} {
		for _, opt := range strings.Split(opts, ",") {
			name, value, _ := strings.Cut(opt, "=")
			switch name {
			case "":
			case "optional":
				s.optional = true
			case "default":
				s.def, s.hasDef = value, true
			case "env":
				s.env = value
			case "omitempty", "string":
			default:
				l.errs = append(l.errs, fmt.Errorf("conf: %s%s: tag option %q is not read here; check that rule in code", path, f.Name, opt))
			}
		}
	}
	if tag, ok := f.Tag.Lookup("env"); ok {
		name, opts, _ := strings.Cut(tag, ",")
		s.env = name
		for _, opt := range strings.Split(opts, ",") {
			switch opt {
			case "secret":
				s.secret = true
			case "file":
				s.fromFile = true
			}
		}
	}
	return s, true
}

func mapping(n *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return out
}

func (l *serviceLoader) object(path string, node *yaml.Node, v reflect.Value) {
	if node != nil && node.Kind != yaml.MappingNode {
		l.errs = append(l.errs, fmt.Errorf("conf: %s:%d: %s is not a mapping", l.file, node.Line, strings.TrimSuffix(path, ".")))
		return
	}
	children := mapping(node)
	read := map[string]bool{}
	l.fields(path, children, read, v)
	if node == nil {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if k := node.Content[i]; !read[k.Value] {
			l.errs = append(l.errs, fmt.Errorf("conf: %s:%d: %s%s is not a field of %s", l.file, k.Line, path, k.Value, v.Type()))
		}
	}
}

func (l *serviceLoader) fields(path string, children map[string]*yaml.Node, read map[string]bool, v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		s, ok := l.spec(path, f)
		if !ok {
			continue
		}
		fv := v.Field(i)
		if s.inline {
			l.fields(path, children, read, fv)
			continue
		}
		child, present := children[s.key]
		read[s.key] = present
		if fv.Kind() == reflect.Struct {
			if !present && s.optional {
				l.absentBlock(path+s.key, fv.Type())
				continue
			}
			l.object(path+s.key+".", child, fv)
			continue
		}
		l.leaf(path+s.key, s, child, present, fv)
	}
}

func (l *serviceLoader) absentBlock(block string, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		s, _ := l.spec("", f)
		switch {
		case s.inline || f.Type.Kind() == reflect.Struct:
			l.absentBlock(block, f.Type)
		case s.env != "":
			if raw, _ := l.lookup(s.env); raw != "" {
				l.unread = append(l.unread, [2]string{s.env, block})
			}
		}
	}
}

func (l *serviceLoader) leaf(key string, s fieldSpec, node *yaml.Node, present bool, fv reflect.Value) {
	switch fv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Chan:
		l.errs = append(l.errs, fmt.Errorf("conf: %s is a %s, which a config file cannot hold", key, fv.Kind()))
		return
	}
	set := false
	if present && node.ShortTag() == "!!null" {
		present = false
	}
	if present {
		if strings.Contains(scalarText(node), "${") {
			l.errs = append(l.errs, fmt.Errorf("conf: %s:%d: %s holds ${...}; nothing is expanded, read the value through an env tag instead", l.file, node.Line, key))
			return
		}
		if s.secret {
			l.errs = append(l.errs, fmt.Errorf("conf: %s:%d: %s is a secret and may come only from $%s, not the file", l.file, node.Line, key, s.env))
			return
		}
		if err := decodeStrict(node, fv.Addr().Interface()); err != nil {
			l.errs = append(l.errs, fmt.Errorf("conf: %s:%d: %s: %w", l.file, node.Line, key, err))
			return
		}
		set = true
	}
	if s.env != "" {
		if raw, ok := l.lookup(s.env); ok {
			l.read[s.env] = true
			if s.fromFile {
				b, err := l.readFile(raw)
				if err != nil {
					l.errs = append(l.errs, fmt.Errorf("conf: %s: $%s names %q: %w", key, s.env, raw, err))
					return
				}
				raw = strings.TrimSpace(string(b))
			}
			if raw != "" || s.optional {
				if err := confvalue.Assign(fv, raw); err != nil {
					l.errs = append(l.errs, fmt.Errorf("conf: %s from $%s: %w", key, s.env, err))
					return
				}
				set = true
			}
		}
	}
	if !set && s.hasDef {
		if err := confvalue.Assign(fv, s.def); err != nil {
			l.errs = append(l.errs, fmt.Errorf("conf: %s default %q: %w", key, s.def, err))
		}
		return
	}
	if !set && !s.optional {
		name := key
		if s.env != "" {
			name += " ($" + s.env + ")"
		}
		l.missing = append(l.missing, name)
	}
}

func decodeStrict(node *yaml.Node, dst any) error {
	b, _ := yaml.Marshal(node)
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(dst)
}

func scalarText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	var b strings.Builder
	for _, c := range n.Content {
		b.WriteString(scalarText(c))
	}
	return b.String()
}
