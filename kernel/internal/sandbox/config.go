package sandbox

type Allowlist struct {
	FS           bool `yaml:"fs" json:"fs"`
	Shell        bool `yaml:"shell" json:"shell"`
	HTTP         bool `yaml:"http" json:"http"`
	Clipboard    bool `yaml:"clipboard" json:"clipboard"`
	Notification bool `yaml:"notification" json:"notification"`
}

type Config struct {
	CSP             string    `yaml:"csp" json:"csp"` // "none", "default", "strict"
	AllowRightClick bool      `yaml:"allow_right_click" json:"allow_right_click"`
	Allowlist       Allowlist `yaml:"allowlist" json:"allowlist"`
}
