//go:build linux

package app

import (
	"os"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/internal/driver/webkitgtk"
	"github.com/tradalab/scorix/window"
)

// WebKitGTK is experimental and needs the webkit2gtk-4.1/4.0 runtime;
// SCORIX_DRIVER=headless opts out.
func defaultDriver() window.Driver {
	if os.Getenv("SCORIX_DRIVER") == "headless" {
		return headless.New()
	}
	return webkitgtk.New()
}
