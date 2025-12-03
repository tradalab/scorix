//go:build linux
// +build linux

package linux

import (
	"fmt"

	"github.com/tradalab/scorix/internal/window"
	webviewgo "github.com/webview/webview_go"
)

type webkitgtk struct {
	wv webviewgo.WebView
}

func New(cfg window.Config) (window.Window, error) {
	wv := webviewgo.New(cfg.Debug)
	if wv == nil {
		return nil, fmt.Errorf("cannot create linux webview")
	}

	wv.SetTitle(cfg.Title)
	wv.SetSize(cfg.Width, cfg.Height, webviewgo.HintNone)

	return &webkitgtk{wv: wv}, nil
}

func (w *webkitgtk) LoadHTML(html string)                  { w.wv.SetHtml(html) }
func (w *webkitgtk) LoadURL(url string)                    { w.wv.Navigate(url) }
func (w *webkitgtk) Init(js string)                        { w.wv.Init(js) }
func (w *webkitgtk) Eval(js string)                        { w.wv.Eval(js) }
func (w *webkitgtk) Close()                                { w.wv.Terminate(); w.wv.Destroy() }
func (w *webkitgtk) Run()                                  { w.wv.Run() }
func (w *webkitgtk) SetTitle(title string)                 { w.wv.SetTitle(title) }
func (w *webkitgtk) SetSize(width, heigh int)              { w.wv.SetSize(width, heigh, webviewgo.HintNone) }
func (w *webkitgtk) Show()                                 { /* Linux always visible */ }
func (w *webkitgtk) Hide()                                 { /* Not supported */ }
func (w *webkitgtk) Center()                               { /* Not supported */ }
func (w *webkitgtk) Bind(name string, f interface{}) error { return w.wv.Bind(name, f) }
func (w *webkitgtk) Unbind(name string) error              { return w.wv.Unbind(name) }
