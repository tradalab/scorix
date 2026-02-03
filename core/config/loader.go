package config

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/tradalab/scorix/internal/logger"
	"gopkg.in/yaml.v3"
)

// //////////////////////////////////////////////////////////////////////////////////////////////////
// PUBLIC API

// Load loads config from a file path.
func Load(path string) (*Config, error) {
	data, err := tryReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadConfigInternal(data, path)
}

// FromBytes loads config from raw YAML bytes.
func FromBytes(data []byte) (*Config, error) {
	return loadConfigInternal(data, "")
}

// //////////////////////////////////////////////////////////////////////////////////////////////////
// INTERNAL CORE LOADER

func loadConfigInternal(data []byte, path string) (*Config, error) {
	cfg := DefaultConfig()

	cfg.path = path // may be empty if using raw data

	// 1. Load from YAML file or embedded data
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
		logger.Info("config loaded",
			logger.Str("path", cfg.path),
			logger.Bool("embedded", path == ""),
		)
	} else {
		logger.Info("no config file or data, using defaults")
	}

	// 2. Override with environment variables
	overrideFromEnv(cfg)

	// 3. Validate after all overrides
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// 4. Build Raw map[string]any
	if err := buildRaw(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// //////////////////////////////////////////////////////////////////////////////////////////////////
// HELPERS

func tryReadFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		// file not found is allowed → use defaults
		return nil, nil
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

	var cfgMap map[string]any
	if err := json.Unmarshal(raw, &cfgMap); err != nil {
		return err
	}

	cfg.Raw = cfgMap
	return nil
}

// ////////////////////////////////////////////////////////////////////////////////////////////////////
// ENV OVERRIDES

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
