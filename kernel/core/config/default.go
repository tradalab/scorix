package config

func DefaultConfig() *Config {
	cfg := &Config{}
	cfg.App.Name = "SCORIX App"
	cfg.App.Version = "1.0.0"
	cfg.Window.Title = "SCORIX"
	cfg.Window.Width = 1000
	cfg.Window.Height = 700
	cfg.Window.Debug = false
	cfg.Dev.HotReload = false
	cfg.Security.CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';"
	cfg.Security.AllowRightClick = false
	//cfg.Security.Allowlist = map[string]bool{
	//	"fs":    false,
	//	"shell": false,
	//	"http":  true,
	//}
	cfg.Plugins = make(map[string]PluginConfig)
	cfg.path = "etc/scorix.yaml"
	return cfg
}
