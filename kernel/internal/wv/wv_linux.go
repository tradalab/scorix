//go:build linux

package wv

import (
	"github.com/tradalab/scorix/kernel/internal/window"
	"github.com/tradalab/scorix/kernel/internal/wv/linux"
)

func newWebView(cfg window.Config) (window.Window, error) {
	return linux.New(cfg)
}
