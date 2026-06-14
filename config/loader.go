package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

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

	overrideFromEnv(cfg)

	// Validate after env overrides are applied.
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
		// Only a not-exist error means "use defaults"; any other stat
		// error (permission denied, I/O error, etc.) must propagate so a
		// real-but-unreadable config is not silently ignored.
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

	// NOTE: JSON round-trip coerces all numbers to float64. Raw is consumed
	// via type-asserting accessors (e.g. GetString), so this is acceptable.
	var cfgMap map[string]any
	if err := json.Unmarshal(raw, &cfgMap); err != nil {
		return err
	}

	// cfg.Raw is a yaml:",inline" field capturing unknown/custom top-level
	// keys. Merge the typed view into it rather than overwriting, so inline-only
	// keys survive while typed values win for known keys.
	if cfg.Raw == nil {
		cfg.Raw = make(map[string]any, len(cfgMap))
	}
	for k, v := range cfgMap {
		cfg.Raw[k] = v
	}
	return nil
}

func overrideFromEnv(cfg *Config) {
	getInt := func(env string) (int, bool) {
		v := os.Getenv(env)
		if v == "" {
			return 0, false
		}
		i, err := strconv.Atoi(v)
		return i, err == nil && i > 0
	}

	if v := os.Getenv("SCORIX_APP_NAME"); v != "" {
		cfg.App.Name = v
	}
	if v := os.Getenv("SCORIX_APP_VERSION"); v != "" {
		cfg.App.Version = v
	}
	if v := os.Getenv("SCORIX_WINDOW_TITLE"); v != "" {
		cfg.Window.Title = v
	}
	if w, ok := getInt("SCORIX_WINDOW_WIDTH"); ok {
		cfg.Window.Width = w
	}
	if h, ok := getInt("SCORIX_WINDOW_HEIGHT"); ok {
		cfg.Window.Height = h
	}
	if v := os.Getenv("SCORIX_WINDOW_DEBUG"); v == "true" {
		cfg.Window.Debug = true
	}
	if v := os.Getenv("SCORIX_DEV_HOT_RELOAD"); v == "true" {
		cfg.Dev.HotReload = true
	}
}
