//go:build darwin
// +build darwin

package darwin

import (
	"fmt"

	"github.com/tradalab/scorix/internal/window"
	webviewgo "github.com/webview/webview_go"
)

type webkit struct {
	wv webviewgo.WebView
}

func New(cfg window.Config) (window.Window, error) {
	wv := webviewgo.New(cfg.Debug)
	if wv == nil {
		return nil, fmt.Errorf("cannot create darwin webview")
	}

	wv.SetTitle(cfg.Title)
	wv.SetSize(cfg.Width, cfg.Height, webviewgo.HintNone)

	return &webkit{wv: wv}, nil
}

func (w *webkit) LoadHTML(html string)                  { w.wv.SetHtml(html) }
func (w *webkit) LoadURL(url string)                    { w.wv.Navigate(url) }
func (w *webkit) Init(js string)                        { w.wv.Init(js) }
func (w *webkit) Eval(js string)                        { w.wv.Eval(js) }
func (w *webkit) Close()                                { w.wv.Terminate(); w.wv.Destroy() }
func (w *webkit) Run()                                  { w.wv.Run() }
func (w *webkit) SetTitle(title string)                 { w.wv.SetTitle(title) }
func (w *webkit) SetSize(width, heigh int)              { w.wv.SetSize(width, heigh, webviewgo.HintNone) }
func (w *webkit) Show()                                 { /* macOS always visible */ }
func (w *webkit) Hide()                                 { /* Not supported */ }
func (w *webkit) Center()                               { /* Not supported */ }
func (w *webkit) Bind(name string, f interface{}) error { return w.wv.Bind(name, f) }
func (w *webkit) Unbind(name string) error              { return w.wv.Unbind(name) }
