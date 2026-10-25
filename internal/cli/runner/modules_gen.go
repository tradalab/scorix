package runner

import "sort"

type builtinModule struct {
	Import  string
	Package string
}

// The built-in modules whose New takes no arguments, by the name scorix.yaml keys them on
// (the module's Name, so http and log differ from their packages).
var builtinModules = map[string]builtinModule{
	"biometric":    {"github.com/tradalab/scorix/module/biometric", "biometric"},
	"browser":      {"github.com/tradalab/scorix/module/browser", "browser"},
	"clipboard":    {"github.com/tradalab/scorix/module/clipboard", "clipboard"},
	"dialog":       {"github.com/tradalab/scorix/module/dialog", "dialog"},
	"env":          {"github.com/tradalab/scorix/module/env", "env"},
	"fs":           {"github.com/tradalab/scorix/module/fs", "fs"},
	"http":         {"github.com/tradalab/scorix/module/httpfetch", "httpfetch"},
	"log":          {"github.com/tradalab/scorix/module/logfwd", "logfwd"},
	"notification": {"github.com/tradalab/scorix/module/notification", "notification"},
	"scanner":      {"github.com/tradalab/scorix/module/scanner", "scanner"},
	"share":        {"github.com/tradalab/scorix/module/share", "share"},
	"store":        {"github.com/tradalab/scorix/module/store", "store"},
	"template":     {"github.com/tradalab/scorix/module/template", "template"},
	"updater":      {"github.com/tradalab/scorix/module/updater", "updater"},
}

// A section enables its module only with `enabled: true`, the same test the runtime makes.
func enabledBuiltins(cfg *ProjectConfig) []builtinModule {
	var names []string
	if cfg != nil {
		for name, section := range cfg.Modules {
			m, _ := section.(map[string]any)
			if on, _ := m["enabled"].(bool); on {
				if _, ok := builtinModules[name]; ok {
					names = append(names, name)
				}
			}
		}
	}
	sort.Strings(names)
	out := make([]builtinModule, 0, len(names))
	for _, n := range names {
		out = append(out, builtinModules[n])
	}
	return out
}
