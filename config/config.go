package config

import (
	"io/fs"

	"github.com/go-playground/validator/v10"
	"github.com/tradalab/scorix/logger"
)

type Config struct {
	// Identifier is SEALED (no `env` tag): it keys the data dir + OS keychain, so a
	// runtime change would point the app at another app's data/secrets.
	App struct {
		Name           string `yaml:"name" json:"name" env:""`
		Version        string `yaml:"version" json:"version" env:""`
		Identifier     string `yaml:"identifier" json:"identifier"`
		SingleInstance bool   `yaml:"single_instance" json:"single_instance"`
	} `yaml:"app" json:"app"`

	Dev struct {
		HotReload bool `yaml:"hot_reload" json:"hot_reload" env:""`
	} `yaml:"dev" json:"dev"`

	// Mode is advisory only (SEALED); the real backend is chosen by entrypoint
	// (Run vs RunWeb), no code branches on it.
	Mode string `yaml:"mode" json:"mode" validate:"omitempty,oneof=app web"` // app, web

	Web struct {
		Host string `yaml:"host" json:"host" env:""`
		Port int    `yaml:"port" json:"port" env:"" validate:"omitempty,gte=0,lte=65535"`
	} `yaml:"web" json:"web"`

	Window   WindowConfig  `yaml:"window" json:"window"`
	Logger   LoggerConfig  `yaml:"logger" json:"logger"`
	Security SandboxConfig `yaml:"security" json:"security"`

	// Values are free-form so each module package defines its own shape.
	Modules map[string]any `yaml:"modules" json:"modules"`

	Raw map[string]any `yaml:",inline" json:",inline"`

	AssetFs     fs.FS  `json:"-" yaml:"-"`
	AssetFsPath string `json:"-" yaml:"-"`
	path        string
}

type WindowConfig struct {
	Title       string `yaml:"title" json:"title" env:""`
	Width       int    `yaml:"width" json:"width" env:""`
	Height      int    `yaml:"height" json:"height" env:""`
	Debug       bool   `yaml:"debug" json:"debug" env:""`
	HideOnClose bool   `yaml:"hide_on_close" json:"hide_on_close" env:""`
}

type LoggerConfig struct {
	Level   string `yaml:"level" json:"level" env:"" validate:"omitempty,oneof=debug info warn error"` // debug, info, warn, error
	Format  string `yaml:"format" json:"format" env:"" validate:"omitempty,oneof=console json"`        // console, json
	Output  string `yaml:"output" json:"output" env:"" validate:"omitempty,oneof=stdout file both"`    // stdout, file, both
	File    string `yaml:"file" json:"file" env:""`                                                    // logs/app.log
	MaxSize int    `yaml:"max_size" json:"max_size"`                                                   // MB
	MaxAge  int    `yaml:"max_age" json:"max_age"`                                                     // days
}

type Allowlist struct {
	FS           bool `yaml:"fs" json:"fs"`
	Shell        bool `yaml:"shell" json:"shell"`
	HTTP         bool `yaml:"http" json:"http"`
	Clipboard    bool `yaml:"clipboard" json:"clipboard"`
	Notification bool `yaml:"notification" json:"notification"`
}

// SandboxConfig is entirely SEALED (no `env` tags): env/overlay must not loosen
// CSP or the capability allowlist (RCE/priv-esc vector). Changing it needs a
// rebuild with a new embedded manifest.
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
