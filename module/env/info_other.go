//go:build !windows

package env

import (
	"os"
	"strings"
)

func osLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			v = strings.SplitN(v, ".", 2)[0]
			return strings.ReplaceAll(v, "_", "-")
		}
	}
	return ""
}

func osDarkMode() *bool { return nil }
