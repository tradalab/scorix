//go:build !windows

package webview2

import (
	"errors"

	"github.com/tradalab/scorix/window"
)

var _ window.Driver = driver{}

// New returns the non-Windows stub: NewRuntime errors so the app can fall back (e.g. to headless).
func New() window.Driver { return driver{} }

type driver struct{}

func (driver) Name() string { return "webview2" }

func (driver) NewRuntime(window.RuntimeConfig) (window.Runtime, error) {
	return nil, errors.New("webview2: only supported on windows")
}
