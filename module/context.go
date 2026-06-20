package module

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/logger"
)

// AppController is nil in headless/web mode.
type AppController interface {
	Show()
	Close()
}

type Context struct {
	IPC        *ModuleIPC // handlers namespaced "<module>:<name>"
	AppName    string
	AppVersion string // app.version; the running version, in one place
	DataDir    string
	App        AppController // nil in web/server mode

	name        string
	rawConfig   map[string]any // embedded modules.<name> (trusted)
	fileOverlay map[string]any // runtime-file modules.<name> (untrusted; env-tagged fields only)
}

func newContext(name string, ipcCore Core, appName, appVersion, dataDir string, rawModuleCfg, fileOverlay map[string]any, appCtrl AppController) *Context {
	return &Context{
		IPC:         NewModuleIPC(name, ipcCore),
		AppName:     appName,
		AppVersion:  appVersion,
		DataDir:     dataDir,
		App:         appCtrl,
		name:        name,
		rawConfig:   rawModuleCfg,
		fileOverlay: fileOverlay,
	}
}

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
