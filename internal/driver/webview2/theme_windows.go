//go:build windows

package webview2

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	dwmwaUseImmersiveDarkMode    = 20 // Windows 10 20H1+
	dwmwaUseImmersiveDarkModeOld = 19 // 1809..1909 used the undocumented slot
)

func osPrefersDark() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

func applyTitlebarTheme(hwnd windows.Handle, theme string) {
	var dark int32
	switch theme {
	case "dark":
		dark = 1
	case "light":
		dark = 0
	default:
		if osPrefersDark() {
			dark = 1
		}
	}
	size := uintptr(unsafe.Sizeof(dark))
	if hr, _, _ := procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmwaUseImmersiveDarkMode,
		uintptr(unsafe.Pointer(&dark)), size); hr != 0 {
		procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmwaUseImmersiveDarkModeOld,
			uintptr(unsafe.Pointer(&dark)), size)
	}
}
