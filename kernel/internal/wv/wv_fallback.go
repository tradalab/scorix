//go:build (!windows && !darwin && !linux) || !cgo || server

package wv

import (
	"errors"

	"github.com/tradalab/scorix/kernel/internal/window"
)

var ErrUnsupportedPlatform = errors.New("platform not supported")

type fallbackWindow struct{}

func (f *fallbackWindow) LoadHTML(html string)                  {}
func (f *fallbackWindow) LoadURL(url string)                    {}
func (f *fallbackWindow) Init(js string)                        {}
func (f *fallbackWindow) Eval(js string)                        {}
func (f *fallbackWindow) Close()                                {}
func (f *fallbackWindow) Run()                                  {}
func (f *fallbackWindow) SetTitle(title string)                 {}
func (f *fallbackWindow) SetSize(w, h int)                      {}
func (f *fallbackWindow) Show()                                 {}
func (f *fallbackWindow) Hide()                                 {}
func (f *fallbackWindow) Center()                               {}
func (f *fallbackWindow) SetHideOnClose(enable bool)            {}
func (f *fallbackWindow) Bind(name string, cb interface{}) error { return nil }
func (f *fallbackWindow) Unbind(name string) error               { return nil }

func newWebView(cfg window.Config) (window.Window, error) {
	return &fallbackWindow{}, ErrUnsupportedPlatform
}
