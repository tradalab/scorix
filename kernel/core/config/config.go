package config

import (
	"io/fs"

	"github.com/go-playground/validator/v10"
	"github.com/tradalab/scorix/kernel/internal/logger"
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

	// Modules holds per-module config blocks.
	// Each key is the module name; the value is a free-form map
	// so module packages can define their own config shape.
	//
	// Example app.yaml:
	//   modules:
	//     gorm:
	//       enabled: true
	//       dsn: app.dat
	Modules map[string]any `yaml:"modules" json:"modules"`

	Raw map[string]any `yaml:",inline" json:",inline"`

	AssetFs     fs.FS
	AssetFsPath string
	path        string
}

type WindowConfig struct {
	Title  string `yaml:"title" json:"title"`
	Width  int    `yaml:"width" json:"width"`
	Height int    `yaml:"height" json:"height"`
	Debug  bool   `yaml:"debug" json:"debug"`
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

func (c *Config) Validate() error {
	v := validator.New()
	if err := v.Struct(c); err != nil {
		return err
	}
	logger.Info("config validated")
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
