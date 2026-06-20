package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Source is where a resolved field value came from, for tracing / `scorix config`.
type Source string

const (
	SourceDefault Source = "default"      // embedded/default already in the struct (untouched)
	SourceFile    Source = "runtime_file" // overridden by the runtime overlay file
	SourceEnv     Source = "env"          // overridden by an environment variable
	SourceEnvFile Source = "env_file"     // env held a path; value read from that file (K8s secret)
	SourceSealed  Source = "sealed"       // no `env` tag — never overridable (override attempts ignored)
)

// Callers MUST mask Value when Secret is true before display/logging.
type FieldResolution struct {
	JSONKey string
	EnvName string // "" when Sealed
	Source  Source
	Secret  bool
	Sealed  bool
	Value   any
}

// The zero value resolves env-only against the real environment.
type ResolveOptions struct {
	// Auto env-name prefix for empty `env` tags, e.g. "SCORIX_MODULE_SQLX_".
	Prefix string
	// Runtime-file section keyed by json name. Only `env`-tagged fields consult it;
	// sealed fields never do — this is what makes "no tag = not overridable" hold.
	FileOverlay map[string]any
	LookupEnv   func(string) (string, bool) // nil → real os funcs (injectable for tests)
	ReadFile    func(string) ([]byte, error)
	Warnf       func(string, ...any)  // gets a message when an overlay key hits a sealed field (override dropped)
	Report      func(FieldResolution) // called once per field with how it resolved
}

// ResolveOverrides uses `env` struct tags as a secure-by-default allowlist; only
// tagged fields of out (pointer to struct) are ever touched, in place.
//
//   - no `env` tag → SEALED: never overridable (embedded/default only).
//   - `env:""`     → overridable; auto env name = Prefix + UPPER(jsonKey).
//   - `env:"NAME"` → overridable via env var NAME.
//   - options after the name (comma-separated):
//     secret   → env-only (never the runtime file) + caller masks it.
//     required → error if still zero after resolution.
//     file     → env var holds a path; file contents are the value.
//
// Per-field precedence: env > runtime_file > in-struct (embedded > default, since
// callers run defaults() first).
func ResolveOverrides(out any, opts ResolveOptions) error {
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}

	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: ResolveOverrides needs a non-nil pointer to struct, got %T", out)
	}
	sv := rv.Elem()
	st := sv.Type()

	tagged := make(map[string]bool)
	var reqMissing []string

	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.PkgPath != "" {
			continue
		}
		jsonKey := jsonFieldName(f)
		fv := sv.Field(i)

		envTag, hasEnv := f.Tag.Lookup("env")
		// json:"-" has no stable external key; treat as sealed even if `env`-tagged.
		if f.Tag.Get("json") == "-" {
			hasEnv = false
		}
		if !hasEnv {
			if opts.FileOverlay != nil {
				if _, present := opts.FileOverlay[jsonKey]; present && opts.Warnf != nil {
					opts.Warnf("config: ignoring runtime override of sealed field %q (no env tag)", jsonKey)
				}
			}
			if opts.Report != nil {
				opts.Report(FieldResolution{JSONKey: jsonKey, Source: SourceSealed, Sealed: true, Value: fv.Interface()})
			}
			continue
		}

		name, secret, required, fromFile := parseEnvTag(envTag, opts.Prefix, jsonKey)
		tagged[jsonKey] = true

		// Only scalar/[]string/Duration leaves resolve from a flat override; warn
		// and skip nested struct/ptr/map rather than failing the whole load.
		if !assignable(fv) {
			if opts.Warnf != nil {
				opts.Warnf("config: env tag on field %q of unsupported kind %s — only scalar/[]string/Duration leaves are overridable; ignoring", jsonKey, fv.Kind())
			}
			continue
		}

		var (
			rawVal any
			source = SourceDefault
		)
		if ev, ok := opts.LookupEnv(name); ok {
			if fromFile {
				b, err := opts.ReadFile(ev)
				if err != nil {
					return fmt.Errorf("config: %s: read file %q: %w", name, ev, err)
				}
				rawVal, source = strings.TrimSpace(string(b)), SourceEnvFile
			} else {
				rawVal, source = ev, SourceEnv
			}
		} else if !secret && opts.FileOverlay != nil {
			// Secrets never come from the (committable) file — only env / mounted path.
			if v, ok := opts.FileOverlay[jsonKey]; ok {
				rawVal, source = v, SourceFile
			}
		}

		if source != SourceDefault {
			if err := assign(fv, rawVal); err != nil {
				return fmt.Errorf("config: %s (%s): %w", jsonKey, name, err)
			}
		}
		if required && fv.IsZero() {
			reqMissing = append(reqMissing, name)
		}
		if opts.Report != nil {
			opts.Report(FieldResolution{JSONKey: jsonKey, EnvName: name, Source: source, Secret: secret, Value: fv.Interface()})
		}
	}

	if len(reqMissing) > 0 {
		return fmt.Errorf("config: required override(s) not set: %s", strings.Join(reqMissing, ", "))
	}
	return nil
}

// EnvVar describes one field for `scorix config --env`.
type EnvVar struct {
	Name     string // "" when Sealed
	JSONKey  string
	Secret   bool
	File     bool
	Required bool
	Sealed   bool
}

// DescribeEnv reports out's env surface from `env` tags without reading the
// environment; sealed fields come back with Sealed=true.
func DescribeEnv(out any, prefix string) []EnvVar {
	rv := reflect.ValueOf(out)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	st := rv.Type()
	var vars []EnvVar
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.PkgPath != "" {
			continue
		}
		jsonKey := jsonFieldName(f)
		envTag, hasEnv := f.Tag.Lookup("env")
		if !hasEnv {
			vars = append(vars, EnvVar{JSONKey: jsonKey, Sealed: true})
			continue
		}
		name, secret, required, fromFile := parseEnvTag(envTag, prefix, jsonKey)
		vars = append(vars, EnvVar{Name: name, JSONKey: jsonKey, Secret: secret, File: fromFile, Required: required})
	}
	return vars
}

func parseEnvTag(tag, prefix, jsonKey string) (name string, secret, required, fromFile bool) {
	parts := strings.Split(tag, ",")
	name = strings.TrimSpace(parts[0])
	if name == "" {
		name = prefix + strings.ToUpper(jsonKey)
	}
	for _, o := range parts[1:] {
		switch strings.TrimSpace(o) {
		case "secret":
			secret = true
		case "required":
			required = true
		case "file":
			fromFile = true
		}
	}
	return
}

func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return f.Name
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

var durationType = reflect.TypeOf(time.Duration(0))

func assignable(fv reflect.Value) bool {
	if fv.Type() == durationType {
		return true
	}
	switch fv.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return fv.Type().Elem().Kind() == reflect.String
	}
	return false
}

func assign(fv reflect.Value, v any) error {
	// time.Duration is reflect.Int64 underneath but must accept "30s", not raw ns.
	if fv.Type() == durationType {
		d, err := toDuration(v)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(toString(v))
	case reflect.Bool:
		b, err := toBool(v)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := toInt(v)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := toInt(v)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid unsigned value %v", v)
		}
		fv.SetUint(uint64(n))
	case reflect.Float32, reflect.Float64:
		fl, err := toFloat(v)
		if err != nil {
			return err
		}
		fv.SetFloat(fl)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element %s", fv.Type().Elem().Kind())
		}
		fv.Set(reflect.ValueOf(toStringSlice(v)))
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case string:
		return time.ParseDuration(strings.TrimSpace(x))
	case int:
		return time.Duration(x), nil
	case int64:
		return time.Duration(x), nil
	case float64:
		return time.Duration(int64(x)), nil
	default:
		return 0, fmt.Errorf("cannot read %v as duration", v)
	}
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case int:
		return x != 0, nil
	case int64:
		return x != 0, nil
	case float64: // YAML/JSON numbers decode to float64
		return x != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "yes", "on", "y":
			return true, nil
		case "no", "off", "n":
			return false, nil
		}
		// strconv handles 1/0/t/f/true/false; YAML spellings above so `debug: yes` loads.
		return strconv.ParseBool(strings.TrimSpace(x))
	default:
		return false, fmt.Errorf("cannot read %v as bool", v)
	}
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64: // YAML/JSON numbers decode to float64
		return int64(x), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	default:
		return 0, fmt.Errorf("cannot read %v as int", v)
	}
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(x), 64)
	default:
		return 0, fmt.Errorf("cannot read %v as float", v)
	}
}

// toStringSlice accepts a comma-separated env string or a parsed YAML list.
func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, toString(e))
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out
	default:
		return nil
	}
}
