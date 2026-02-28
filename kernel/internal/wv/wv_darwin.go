//go:build darwin

package wv

import (
	"github.com/tradalab/scorix/kernel/internal/window"
	"github.com/tradalab/scorix/kernel/internal/wv/darwin"
)

func newWebView(cfg window.Config) (window.Window, error) {
	return darwin.New(cfg)
}
