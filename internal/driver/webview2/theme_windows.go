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
	dwmwaSystemBackdropType      = 38 // Windows 11 22H2+
)

func applyBackdrop(hwnd windows.Handle, backdrop string) {
	v, ok := map[string]int32{"mica": 2, "acrylic": 3, "tabbed": 4, "none": 1}[backdrop]
	if !ok {
		return
	}
	procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmwaSystemBackdropType,
		uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Sizeof(v))) // silently absent before Win11 22H2
}

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
