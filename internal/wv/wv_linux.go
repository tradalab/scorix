//go:build linux
// +build linux

package wv

import (
	"github.com/tradalab/scorix/internal/window"
	"github.com/tradalab/scorix/internal/wv/linux"
)

func newWindow(cfg window.Config) (window.Window, error) {
	return linux.New(cfg)
}
