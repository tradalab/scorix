//go:build ios && cgo

package app

import (
	"github.com/tradalab/scorix/internal/driver/ioswebview"
	"github.com/tradalab/scorix/window"
)

func defaultDriver() window.Driver { return ioswebview.New() }
