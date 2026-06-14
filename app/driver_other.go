//go:build !windows && !darwin && !linux

package app

import (
	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/window"
)

// No native backend on these platforms (BSDs, …) — web mode and tests only.
func defaultDriver() window.Driver { return headless.New() }
