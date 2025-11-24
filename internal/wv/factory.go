package wv

import (
	"errors"
	"runtime"

	"github.com/tradalab/scorix/internal/window"
	"github.com/tradalab/scorix/internal/wv/darwin"
	"github.com/tradalab/scorix/internal/wv/linux"
	"github.com/tradalab/scorix/internal/wv/windows"
)

var ErrUnsupportedPlatform = errors.New("platform not supported")

func newWebView(cfg window.Config) (window.Window, error) {
	switch runtime.GOOS {
	case "windows":
		return windows.New(cfg)
	case "darwin":
		return darwin.New(cfg)
	case "linux":
		return linux.New(cfg)
	default:
		return nil, ErrUnsupportedPlatform
	}
}
