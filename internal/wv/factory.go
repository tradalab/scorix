package wv

import (
	"errors"
	"runtime"

	"github.com/tradalab/scorix/internal/window"
)

var ErrUnsupportedPlatform = errors.New("platform not supported")

func newWebView(cfg window.Config) (window.Window, error) {
	switch runtime.GOOS {
	case "windows":
		return newWindow(cfg)
	case "darwin":
		return newWindow(cfg)
	case "linux":
		return newWindow(cfg)
	default:
		return nil, ErrUnsupportedPlatform
	}
}
