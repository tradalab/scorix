package module

// ModuleConfig is the per-module config block from app.yaml.
// Example config:
//
//	modules:
//	  gorm:
//	    enabled: true
//	    dsn: app.dat
type ModuleConfig struct {
	Enabled bool           `yaml:"enabled" json:"enabled"`
	Config  map[string]any `yaml:"config"  json:"config"`
}
