//go:build windows

package windows

import (
	"fmt"
	"syscall"

	"github.com/tradalab/scorix/internal/window"
	webviewgo "github.com/webview/webview_go"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	showWindow          = user32.NewProc("ShowWindow")
)

const (
	SW_RESTORE = 9
)

type webview2 struct {
	wv webviewgo.WebView
}

func New(cfg window.Config) (window.Window, error) {
	wv := webviewgo.New(cfg.Debug)
	if wv == nil {
		return nil, fmt.Errorf("cannot create windows webview")
	}

	wv.SetTitle(cfg.Title)
	wv.SetSize(cfg.Width, cfg.Height, webviewgo.HintNone)

	return &webview2{wv: wv}, nil
}

func (w *webview2) LoadHTML(html string)     { w.wv.SetHtml(html) }
func (w *webview2) LoadURL(url string)       { w.wv.Navigate(url) }
func (w *webview2) Init(js string)           { w.wv.Init(js) }
func (w *webview2) Eval(js string)           { w.wv.Dispatch(func() { w.wv.Eval(js) }) }
func (w *webview2) Close()                   { w.wv.Terminate(); w.wv.Destroy() }
func (w *webview2) Run()                     { w.wv.Run() }
func (w *webview2) SetTitle(title string)    { w.wv.SetTitle(title) }
func (w *webview2) SetSize(width, heigh int) { w.wv.SetSize(width, heigh, webviewgo.HintNone) }
func (w *webview2) Show() {
	// convert unsafe.Pointer → uintptr
	hwnd := uintptr(w.wv.Window())
	if hwnd == 0 {
		return
	}
	// Restore if minimize
	showWindow.Call(hwnd, uintptr(SW_RESTORE))
	// Up to foreground
	setForegroundWindow.Call(hwnd)
}
func (w *webview2) Hide()                                 { /* Not supported */ }
func (w *webview2) Center()                               { /* Not supported */ }
func (w *webview2) Bind(name string, f interface{}) error { return w.wv.Bind(name, f) }
func (w *webview2) Unbind(name string) error              { return w.wv.Unbind(name) }
