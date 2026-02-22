package logger

type Config struct {
	Level   string `yaml:"level" json:"level"`       // debug, info, warn, error
	Format  string `yaml:"format" json:"format"`     // console, json
	Output  string `yaml:"output" json:"output"`     // stdout, file, both
	File    string `yaml:"file" json:"file"`         // logs/app.log
	MaxSize int    `yaml:"max_size" json:"max_size"` // MB
	MaxAge  int    `yaml:"max_age" json:"max_age"`   // days
}
