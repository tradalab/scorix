package sandbox

import (
	"fmt"

	"github.com/tradalab/scorix/internal/logger"
)

// Validate — call before IPC
func Validate(method string) error {
	if !isAllowed(method) {
		logger.Warn("ipc blocked", logger.Str("method", method))
		return fmt.Errorf("command blocked: %s", method)
	}
	return nil
}

func isAllowed(method string) bool {
	switch method {
	case "fs.read", "fs.write":
		return cfg.Allowlist.FS
	case "shell.open":
		return cfg.Allowlist.Shell
	case "http.request":
		return cfg.Allowlist.HTTP
	case "clipboard.read", "clipboard.write":
		return cfg.Allowlist.Clipboard
	case "notification.show":
		return cfg.Allowlist.Notification
	default:
		return true
	}
}
