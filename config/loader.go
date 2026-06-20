package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/tradalab/scorix/logger"
	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := tryReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadConfigInternal(data, path)
}

func FromBytes(data []byte) (*Config, error) {
	return loadConfigInternal(data, "")
}

func loadConfigInternal(data []byte, path string) (*Config, error) {
	cfg := DefaultConfig()

	cfg.path = path

	if len(data) > 0 {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			src := path
			if src == "" {
				src = "embedded"
			}
			return nil, fmt.Errorf("parse config %s: %w", src, err)
		}
		logger.Info("config loaded",
			logger.Str("path", cfg.path),
			logger.Bool("embedded", path == ""),
		)
	} else {
		logger.Info("no config file or data, using defaults")
	}

	// A malformed env override fails the load rather than being silently ignored.
	if err := ApplyEnv(cfg); err != nil {
		return nil, fmt.Errorf("config: apply env overrides: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if err := buildRaw(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func tryReadFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		// Only not-exist means "use defaults"; other errors (perm, I/O) must
		// propagate so a real-but-unreadable config isn't silently ignored.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func buildRaw(cfg *Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	// JSON round-trip coerces numbers to float64; Raw is read via type-asserting
	// accessors, so that's fine.
	var cfgMap map[string]any
	if err := json.Unmarshal(raw, &cfgMap); err != nil {
		return err
	}
	delete(cfgMap, "Raw") // inline field marshals under "Raw"; drop to avoid Raw-in-Raw

	// Merge the typed view into Raw (don't overwrite) so inline-only keys survive
	// while typed values win for known keys.
	if cfg.Raw == nil {
		cfg.Raw = make(map[string]any, len(cfgMap))
	}
	for k, v := range cfgMap {
		cfg.Raw[k] = v
	}
	return nil
}

// ApplyEnv resolves env-tagged framework fields; Security stays sealed. Exported
// so the App can re-run it after merging a runtime overlay (env must still win).
func ApplyEnv(cfg *Config) error {
	return ApplyOverlays(cfg, nil)
}

// RawMap parses YAML into a section map; nil for empty input.
func RawMap(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse runtime config: %w", err)
	}
	return m, nil
}

// ApplyOverlays resolves framework config from env + an optional runtime-file
// section map ("app", "window", ...). Per-field precedence: env > file > embedded.
// Sections resolve independently so each gets its own auto-name prefix.
func ApplyOverlays(cfg *Config, file map[string]any) error {
	warn := func(format string, a ...any) { logger.Warn(fmt.Sprintf(format, a...)) }
	sections := []struct {
		prefix string
		key    string
		target any
	}{
		{"SCORIX_APP_", "app", &cfg.App},
		{"SCORIX_WINDOW_", "window", &cfg.Window},
		{"SCORIX_DEV_", "dev", &cfg.Dev},
		{"SCORIX_WEB_", "web", &cfg.Web},
		{"SCORIX_LOGGER_", "logger", &cfg.Logger},
	}
	for _, s := range sections {
		var overlay map[string]any
		if file != nil {
			overlay = AsStringMap(file[s.key])
		}
		if err := ResolveOverrides(s.target, ResolveOptions{Prefix: s.prefix, FileOverlay: overlay, Warnf: warn}); err != nil {
			return err
		}
	}
	// security is sealed; surface (don't apply) a runtime attempt to change it.
	if file != nil {
		if _, ok := file["security"]; ok {
			warn("config: ignoring runtime overlay of `security` (sealed — rebuild with a new manifest to change CSP/allowlist)")
		}
	}
	return nil
}

// AsStringMap normalises the map[interface{}]any yaml.v3 can produce to
// map[string]any, or nil.
func AsStringMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(map[interface{}]any); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			if ks, ok := k.(string); ok {
				out[ks] = val
			}
		}
		return out
	}
	return nil
}
