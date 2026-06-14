package module

import (
	"encoding/json"
	"strings"
)

// AppController is optional app-level window control; nil in headless/web mode.
type AppController interface {
	Show()
	Close()
}

// Context is passed to a module during OnLoad.
type Context struct {
	// Handlers are namespaced as "<module>:<name>".
	IPC *ModuleIPC

	AppName string

	DataDir string

	// nil in web/server mode.
	App AppController

	// modules.<name> section.
	rawConfig map[string]any
}

func newContext(name string, ipcCore Core, appName, dataDir string, rawModuleCfg map[string]any, appCtrl AppController) *Context {
	return &Context{
		IPC:       NewModuleIPC(name, ipcCore),
		AppName:   appName,
		DataDir:   dataDir,
		App:       appCtrl,
		rawConfig: rawModuleCfg,
	}
}

// GetConfig looks up a dot-path key in the module's config section
// (e.g. "dsn" → modules.<name>.dsn).
func (c *Context) GetConfig(path string) (any, bool) {
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
	b, err := json.Marshal(c.rawConfig)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// toStringMap normalises map[interface{}]any (from YAML) to map[string]any.
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
