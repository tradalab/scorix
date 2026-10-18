//go:build android && cgo

package app

import (
	"github.com/tradalab/scorix/internal/driver/androidwebview"
	"github.com/tradalab/scorix/window"
)

func defaultDriver() window.Driver { return androidwebview.New() }
