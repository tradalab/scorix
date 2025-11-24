package wv

import (
	"github.com/tradalab/scorix/internal/window"
)

// New — public factory
func New(cfg window.Config) (window.Window, error) {
	return newWebView(cfg)
}
