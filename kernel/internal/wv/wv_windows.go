//go:build windows && cgo && !server

package wv

import (
	"github.com/tradalab/scorix/kernel/internal/window"
	"github.com/tradalab/scorix/kernel/internal/wv/windows"
)

func newWebView(cfg window.Config) (window.Window, error) {
	return windows.New(cfg)
}
