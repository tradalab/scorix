package runner

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tradalab/scorix/config"
)

// cfgSection ties a config section to its env prefix and the addressable struct
// ResolveOverrides/DescribeEnv reflect over.
type cfgSection struct {
	name   string
	prefix string
	target any
}

func frameworkSections(cfg *config.Config) []cfgSection {
	return []cfgSection{
		{"app", "SCORIX_APP_", &cfg.App},
		{"window", "SCORIX_WINDOW_", &cfg.Window},
		{"dev", "SCORIX_DEV_", &cfg.Dev},
		{"web", "SCORIX_WEB_", &cfg.Web},
		{"logger", "SCORIX_LOGGER_", &cfg.Logger},
	}
}

func resolveManifestPath(explicit string) (string, error) {
	candidates := []string{explicit, "scorix.yaml", "etc/app.yaml"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no manifest found (looked for scorix.yaml, etc/app.yaml) — pass --manifest")
}

// ConfigResolved prints effective config (env + optional overlay), annotating each
// key with its source and masking secrets. Module sections are dumped as embedded —
// their env surface (SCORIX_MODULE_<NAME>_<KEY>) is resolved in-process at load time.
func ConfigResolved(manifestPath, overlayPath string) error {
	cfg, overlay, mpath, err := loadForInspect(manifestPath, overlayPath)
	if err != nil {
		return err
	}
	fmt.Printf("# resolved config (manifest: %s", mpath)
	if overlayPath != "" {
		fmt.Printf(", overlay: %s", overlayPath)
	}
	fmt.Println(")")

	for _, s := range frameworkSections(cfg) {
		var fileSec map[string]any
		if overlay != nil {
			fileSec = asSection(overlay[s.name])
		}
		fmt.Printf("\n[%s]\n", s.name)
		err := config.ResolveOverrides(s.target, config.ResolveOptions{
			Prefix:      s.prefix,
			FileOverlay: fileSec,
			Report: func(r config.FieldResolution) {
				val := fmt.Sprintf("%v", r.Value)
				if r.Secret {
					val = "***"
				}
				if r.Sealed {
					fmt.Printf("  %-22s = %-20s (sealed)\n", r.JSONKey, val)
					return
				}
				fmt.Printf("  %-22s = %-20s [%s] %s\n", r.JSONKey, val, r.Source, r.EnvName)
			},
		})
		if err != nil {
			return err
		}
	}

	fmt.Printf("\n[security] (sealed — rebuild to change)\n")
	fmt.Printf("  csp                    = %s\n", cfg.Security.CSP)

	if len(cfg.Modules) > 0 {
		fmt.Printf("\n[modules] (embedded; env overrides via SCORIX_MODULE_<NAME>_<KEY>)\n")
		names := make([]string, 0, len(cfg.Modules))
		for n := range cfg.Modules {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("  %s: %v\n", n, cfg.Modules[n])
		}
	}
	return nil
}

// ConfigEnv prints the env surface (from `env` tags). Sealed fields are listed
// too so it's clear what is deliberately NOT overridable.
func ConfigEnv(manifestPath string) error {
	cfg, _, mpath, err := loadForInspect(manifestPath, "")
	if err != nil {
		return err
	}
	fmt.Printf("# env surface (manifest: %s)\n", mpath)

	for _, s := range frameworkSections(cfg) {
		fmt.Printf("\n[%s]\n", s.name)
		for _, v := range config.DescribeEnv(s.target, s.prefix) {
			if v.Sealed {
				fmt.Printf("  (sealed)  %s\n", v.JSONKey)
				continue
			}
			var flags []string
			if v.Secret {
				flags = append(flags, "secret")
			}
			if v.File {
				flags = append(flags, "file")
			}
			if v.Required {
				flags = append(flags, "required")
			}
			suffix := ""
			if len(flags) > 0 {
				suffix = "  (" + strings.Join(flags, ",") + ")"
			}
			fmt.Printf("  %s%s\n", v.Name, suffix)
		}
	}

	if len(cfg.Modules) > 0 {
		fmt.Printf("\n[modules]\n")
		names := make([]string, 0, len(cfg.Modules))
		for n := range cfg.Modules {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			// Can't reflect the module's Go Config struct from YAML — only state the convention.
			fmt.Printf("  %-12s SCORIX_MODULE_%s_<KEY>  (see the module's Config tags for which KEYs are overridable)\n",
				n, strings.ToUpper(n))
		}
	}
	return nil
}

func loadForInspect(manifestPath, overlayPath string) (*config.Config, map[string]any, string, error) {
	mpath, err := resolveManifestPath(manifestPath)
	if err != nil {
		return nil, nil, "", err
	}
	cfg, err := config.Load(mpath)
	if err != nil {
		return nil, nil, "", err
	}
	var overlay map[string]any
	if overlayPath != "" {
		data, err := os.ReadFile(overlayPath)
		if err != nil {
			return nil, nil, "", fmt.Errorf("read overlay %q: %w", overlayPath, err)
		}
		if overlay, err = config.RawMap(data); err != nil {
			return nil, nil, "", err
		}
	}
	return cfg, overlay, mpath, nil
}

// asSection normalises map[interface{}]any from yaml.v3 so --resolved doesn't under-report.
func asSection(v any) map[string]any {
	return config.AsStringMap(v)
}
