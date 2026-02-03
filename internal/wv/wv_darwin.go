//go:build darwin

package wv

import (
	"github.com/tradalab/scorix/internal/window"
	"github.com/tradalab/scorix/internal/wv/darwin"
)

func newWebView(cfg window.Config) (window.Window, error) {
	return darwin.New(cfg)
}
