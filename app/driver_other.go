//go:build (!windows && !darwin && !linux) || (android && !cgo) || ios

package app

import (
	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/window"
)

// No native backend on these platforms (BSDs, iOS, Android without cgo for JNI) - web mode and tests only.
func defaultDriver() window.Driver { return headless.New() }
