package config

func DefaultConfig() *Config {
	cfg := &Config{}
	cfg.App.Name = "SCORIX App"
	cfg.App.Version = "1.0.0"
	cfg.Mode = "app"
	cfg.Web.Host = "127.0.0.1"
	cfg.Web.Port = 0
	cfg.Window.Title = "SCORIX"
	cfg.Window.Width = 1000
	cfg.Window.Height = 700
	cfg.Window.Resizable = true
	cfg.Window.Debug = false
	cfg.Dev.HotReload = false
	cfg.Security.CSP = "default"
	cfg.Security.AllowRightClick = false
	return cfg
}
