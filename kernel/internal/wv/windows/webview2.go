//go:build windows

package windows

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/tradalab/scorix/kernel/internal/window"
	webviewgo "github.com/webview/webview_go"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	shell32             = syscall.NewLazyDLL("shell32.dll")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	showWindow          = user32.NewProc("ShowWindow")
	callWindowProc      = user32.NewProc("CallWindowProcW")
	sendMessage         = user32.NewProc("SendMessageW")
	getModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	extractIcon         = shell32.NewProc("ExtractIconW")
	setWindowLongPtr    *syscall.LazyProc
)

func init() {
	setWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	if err := setWindowLongPtr.Find(); err != nil {
		setWindowLongPtr = user32.NewProc("SetWindowLongW")
	}
}

const (
	SW_RESTORE        = 9
	SW_HIDE           = 0
	GWLP_WNDPROC int32 = -4
	WM_CLOSE          = 0x0010
)

type webview2 struct {
	wv              webviewgo.WebView
	hideOnClose     bool
	originalWndProc uintptr
}

var currentWV *webview2
var wndProcCallback uintptr

func init() {
	if wndProcCallback == 0 {
		wndProcCallback = syscall.NewCallback(wndProc)
	}
}

func wndProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	if currentWV != nil && msg == WM_CLOSE && currentWV.hideOnClose {
		showWindow.Call(uintptr(hwnd), SW_HIDE)
		return 0
	}
	if currentWV != nil && currentWV.originalWndProc != 0 {
		ret, _, _ := callWindowProc.Call(currentWV.originalWndProc, uintptr(hwnd), uintptr(msg), wparam, lparam)
		return ret
	}
	ret, _, _ := syscall.Syscall6(user32.NewProc("DefWindowProcW").Addr(), 4, uintptr(hwnd), uintptr(msg), wparam, lparam, 0, 0)
	return ret
}

func New(cfg window.Config) (window.Window, error) {
	wv := webviewgo.New(cfg.Debug)
	if wv == nil {
		return nil, fmt.Errorf("cannot create windows webview")
	}

	wv.SetTitle(cfg.Title)
	wv.SetSize(cfg.Width, cfg.Height, webviewgo.HintNone)

	instance := &webview2{wv: wv}
	currentWV = instance

	hwnd := uintptr(wv.Window())
	if hwnd != 0 {
		exePath, err := os.Executable()
		if err == nil {
			hInst, _, _ := getModuleHandle.Call(0)
			hIcon, _, _ := extractIcon.Call(hInst, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(exePath))), 0)
			if hIcon != 0 {
				sendMessage.Call(hwnd, 0x0080 /* WM_SETICON */, 0 /* ICON_SMALL */, hIcon)
				sendMessage.Call(hwnd, 0x0080 /* WM_SETICON */, 1 /* ICON_BIG */, hIcon)
			}
		}
	}

	return instance, nil
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
	hwnd := uintptr(w.wv.Window())
	if hwnd == 0 {
		return
	}
	showWindow.Call(hwnd, uintptr(SW_RESTORE))
	setForegroundWindow.Call(hwnd)
}

func (w *webview2) Hide() {
	hwnd := uintptr(w.wv.Window())
	if hwnd != 0 {
		showWindow.Call(hwnd, SW_HIDE)
	}
}

func (w *webview2) SetHideOnClose(enable bool) {
	w.hideOnClose = enable
	hwnd := uintptr(w.wv.Window())
	if hwnd != 0 && w.originalWndProc == 0 {
		gwlp := int(-4)
		ret, _, _ := setWindowLongPtr.Call(hwnd, uintptr(gwlp), wndProcCallback)
		w.originalWndProc = ret
	}
}

func (w *webview2) Center()                               { /* Not supported */ }
func (w *webview2) Bind(name string, f interface{}) error { return w.wv.Bind(name, f) }
func (w *webview2) Unbind(name string) error              { return w.wv.Unbind(name) }
