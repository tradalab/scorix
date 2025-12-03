//go:build windows
// +build windows

package wv

import (
	"github.com/tradalab/scorix/internal/window"
	"github.com/tradalab/scorix/internal/wv/windows"
)

func newWindow(cfg window.Config) (window.Window, error) {
	return windows.New(cfg)
}
