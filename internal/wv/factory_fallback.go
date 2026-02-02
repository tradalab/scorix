//go:build !windows && !darwin && !linux

package wv

import (
	"errors"

	"github.com/tradalab/scorix/internal/window"
)

var ErrUnsupportedPlatform = errors.New("platform not supported")

func newWebView(cfg window.Config) (window.Window, error) {
	return nil, ErrUnsupportedPlatform
}
