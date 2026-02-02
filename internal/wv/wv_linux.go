//go:build linux

package wv

import (
	"github.com/tradalab/scorix/internal/window"
	"github.com/tradalab/scorix/internal/wv/linux"
)

func newWebView(cfg window.Config) (window.Window, error) {
	return linux.New(cfg)
}
