//go:build windows

package wv

import (
	"github.com/tradalab/scorix/internal/window"
	"github.com/tradalab/scorix/internal/wv/windows"
)

func newWebView(cfg window.Config) (window.Window, error) {
	return windows.New(cfg)
}
