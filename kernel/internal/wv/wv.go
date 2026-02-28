package wv

import (
	"github.com/tradalab/scorix/kernel/internal/window"
)

func New(cfg window.Config) (window.Window, error) {
	return newWebView(cfg)
}
