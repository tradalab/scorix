package sandbox

import "github.com/tradalab/scorix/kernel/internal/logger"

var cfg Config

func Init(c Config) {
	cfg = c
	logger.Info("sandbox init", logger.Str("csp", c.CSP))
}

func AllowFS() bool    { return cfg.Allowlist.FS }
func AllowShell() bool { return cfg.Allowlist.Shell }
