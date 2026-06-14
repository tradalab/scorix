//go:build windows

package app

import (
	"github.com/tradalab/scorix/internal/driver/webview2"
	"github.com/tradalab/scorix/window"
)

func defaultDriver() window.Driver { return webview2.New() }
