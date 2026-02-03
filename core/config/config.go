package config

import (
	"io/fs"

	"github.com/go-playground/validator/v10"
	"github.com/tradalab/scorix/internal/logger"
)

type Config struct {
	App struct {
		Name    string `yaml:"name" json:"name"`
		Version string `yaml:"version" json:"version"`
	} `yaml:"app" json:"app"`

	Dev struct {
		HotReload bool `yaml:"hot_reload" json:"hot_reload"`
	} `yaml:"dev" json:"dev"`

	Window   WindowConfig  `yaml:"window" json:"window"`
	Logger   LoggerConfig  `yaml:"logger" json:"logger"`
	Security SandboxConfig `yaml:"security" json:"security"`

	Plugins map[string]PluginConfig `yaml:"plugins" json:"plugins"`

	Extensions struct {
		Updater struct {
			AppcastURL      string `yaml:"appcast_url" json:"appcast_url"`
			PublicKeyBase64 string `yaml:"public_key_base_64" json:"public_key_base_64"`
			PlatformKey     string `yaml:"platform_key" json:"platform_key"`
			ForceElevate    bool   `yaml:"force_elevate" json:"force_elevate"`
			CurrentVersion  string `yaml:"current_version" json:"current_version"`
		} `yaml:"updater" json:"updater"`
	} `yaml:"extensions" json:"extensions"`

	Raw map[string]any `yaml:",inline" json:",inline"`

	AssetFs     fs.FS
	AssetFsPath string
	path        string
}

type WindowConfig struct {
	Title  string `yaml:"title" json:"title" json:"title"`
	Width  int    `yaml:"width" json:"width" json:"width"`
	Height int    `yaml:"height" json:"height" json:"height"`
	Debug  bool   `yaml:"debug" json:"debug" json:"debug"`
}

type LoggerConfig struct {
	Level   string `yaml:"level" json:"level"`       // debug, info, warn, error
	Format  string `yaml:"format" json:"format"`     // console, json
	Output  string `yaml:"output" json:"output"`     // stdout, file, both
	File    string `yaml:"file" json:"file"`         // logs/app.log
	MaxSize int    `yaml:"max_size" json:"max_size"` // MB
	MaxAge  int    `yaml:"max_age" json:"max_age"`   // days
}

type Allowlist struct {
	FS           bool `yaml:"fs" json:"fs"`
	Shell        bool `yaml:"shell" json:"shell"`
	HTTP         bool `yaml:"http" json:"http"`
	Clipboard    bool `yaml:"clipboard" json:"clipboard"`
	Notification bool `yaml:"notification" json:"notification"`
}

type SandboxConfig struct {
	CSP             string    `yaml:"csp" json:"csp"` // "none", "default", "strict"
	AllowRightClick bool      `yaml:"allow_right_click" json:"allow_right_click"`
	Allowlist       Allowlist `yaml:"allowlist" json:"allowlist"`
}

type PluginConfig struct {
	Enabled bool           `yaml:"enabled" json:"enabled"`
	Config  map[string]any `yaml:"config" json:"config"`
}

func (c *Config) Validate() error {
	v := validator.New()
	if err := v.Struct(c); err != nil {
		return err
	}
	logger.Info("config validated")
	return nil
}

func (c *Config) GetPluginConfig(name string) map[string]any {
	if p, ok := c.Plugins[name]; ok && p.Enabled {
		return p.Config
	}
	return nil
}

func (c *Config) GetString(key, def string) string {
	if v, ok := c.Raw[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}
