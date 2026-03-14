package module

import (
	"encoding/json"
	"strings"

	"github.com/tradalab/scorix/kernel/internal/ipc"
)

// Context is passed to a module during OnLoad.
// It provides access to IPC, config, and app-level metadata.
type Context struct {
	// IPC is the module-scoped IPC helper. Handlers are namespaced as "<module>:<name>".
	IPC *ModuleIPC

	// AppName is the application name from config.
	AppName string

	// DataDir is the platform-specific data directory for this app.
	DataDir string

	// rawConfig is the raw config map (modules.<name> section).
	rawConfig map[string]any
}

// newContext creates a Context for a given module.
func newContext(name string, ipcCore *ipc.IPC, appName, dataDir string, rawModuleCfg map[string]any) *Context {
	return &Context{
		IPC:       NewModuleIPC(name, ipcCore),
		AppName:   appName,
		DataDir:   dataDir,
		rawConfig: rawModuleCfg,
	}
}

// GetConfig looks up a dot-path key in the module's config section.
// e.g. GetConfig("dsn") looks up modules.<name>.dsn
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

// GetConfigString returns a string config value or the provided default.
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

// GetConfigBool returns a bool config value or the provided default.
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

// Decode unmarshals the full module config section into a struct.
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
