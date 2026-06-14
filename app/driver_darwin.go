//go:build darwin

package app

import (
	"os"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/internal/driver/wkwebview"
	"github.com/tradalab/scorix/window"
)

// WKWebView is experimental; SCORIX_DRIVER=headless opts out.
func defaultDriver() window.Driver {
	if os.Getenv("SCORIX_DRIVER") == "headless" {
		return headless.New()
	}
	return wkwebview.New()
}
