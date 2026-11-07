package module

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/logger"
)

// AppController is nil in headless/web mode.
type AppController interface {
	Show()
	Close()
	// OnOpenURL subscribes to launches of a declared app.protocols scheme. It is
	// how a module gets a click back from something the OS owns - a toast, a
	// tray balloon - without any of them needing a COM activator.
	OnOpenURL(fn func(url string))
}

type Context struct {
	IPC        *ModuleIPC // handlers namespaced "<module>:<name>"
	AppName    string
	AppVersion string // app.version; the running version, in one place
	// AppIdentifier is app.identifier: the AUMID Windows matches a toast against.
	AppIdentifier string
	// FirstProtocol is app.protocols[0], the scheme a module routes OS callbacks
	// through. Empty when the app declares none.
	FirstProtocol string
	DataDir       string
	App           AppController // nil in web/server mode

	name        string
	read        bool
	rawConfig   map[string]any // embedded modules.<name> (trusted)
	fileOverlay map[string]any // runtime-file modules.<name> (untrusted; env-tagged fields only)
}

func newContext(name string, ipcCore Core, appName, appVersion, appID, protocol, dataDir string, rawModuleCfg, fileOverlay map[string]any, appCtrl AppController) *Context {
	return &Context{
		IPC:           NewModuleIPC(name, ipcCore),
		AppName:       appName,
		AppVersion:    appVersion,
		AppIdentifier: appID,
		FirstProtocol: protocol,
		DataDir:       dataDir,
		App:           appCtrl,
		name:          name,
		rawConfig:     rawModuleCfg,
		fileOverlay:   fileOverlay,
	}
}

func (c *Context) GetConfig(path string) (any, bool) {
	c.read = true
	if c.rawConfig == nil {
		return nil, false
	}
	parts := strings.Split(path, ".")
	var cur any = c.rawConfig
	for _, p := range parts {
		m, ok := toStringMap(cur)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func (c *Context) GetConfigString(path string, def string) string {
	v, ok := c.GetConfig(path)
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func (c *Context) GetConfigBool(path string, def bool) bool {
	v, ok := c.GetConfig(path)
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func (c *Context) Decode(out any) error {
	c.read = true
	b, err := json.Marshal(c.rawConfig)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return err
	}
	for _, key := range unknownKeys(c.rawConfig, reflect.TypeOf(out), "") {
		logger.Warn(fmt.Sprintf("modules.%s: unknown key %q, nothing reads it (typo?)", c.name, key))
	}
	return nil
}

func (c *Context) warnIfNothingRead() {
	if c.read {
		return
	}
	var keys []string
	for key := range c.rawConfig {
		if key != "enabled" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		logger.Warn(fmt.Sprintf("modules.%s: unknown key %q, this module reads no config (typo?)", c.name, key))
	}
}

var unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

func unknownKeys(raw map[string]any, t reflect.Type, prefix string) []string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || reflect.PointerTo(t).Implements(unmarshalerType) {
		return nil
	}
	fields := map[string]reflect.Type{}
	jsonFields(t, fields)
	var out []string
	for key, val := range raw {
		if prefix == "" && key == "enabled" {
			continue
		}
		ft, ok := fields[strings.ToLower(key)]
		if !ok {
			out = append(out, prefix+key)
			continue
		}
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if sub, ok := toStringMap(val); ok {
			out = append(out, unknownKeys(sub, ft, prefix+key+".")...)
		} else if list, ok := val.([]any); ok && (ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array) {
			for i, item := range list {
				if sub, ok := toStringMap(item); ok {
					out = append(out, unknownKeys(sub, ft.Elem(), fmt.Sprintf("%s%s[%d].", prefix, key, i))...)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func jsonFields(t reflect.Type, into map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				jsonFields(et, into)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		into[strings.ToLower(name)] = f.Type
	}
}

// ApplyOverrides applies env + runtime-file overrides via `env` tags. Call AFTER
// Decode + defaults(); precedence env > runtime_file > embedded > default.
// Untagged fields are SEALED (security: updater keys etc. stay immutable at
// runtime). See config.ResolveOverrides for the tag grammar.
func (c *Context) ApplyOverrides(out any) error {
	prefix := "SCORIX_MODULE_" + strings.ToUpper(c.name) + "_"
	return config.ResolveOverrides(out, config.ResolveOptions{
		Prefix:      prefix,
		FileOverlay: c.fileOverlay,
		Warnf:       func(format string, a ...any) { logger.Warn(fmt.Sprintf(format, a...)) },
	})
}

// toStringMap normalises the map[interface{}]any yaml.v3 can produce.
func toStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	if m, ok := v.(map[interface{}]any); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			if ks, ok := k.(string); ok {
				out[ks] = val
			}
		}
		return out, true
	}
	return nil, false
}
