//go:build windows

package env

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func osLocale() string {
	var buf [85]uint16 // LOCALE_NAME_MAX_LENGTH
	r, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}

func osDarkMode() *bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return nil
	}
	dark := v == 0
	return &dark
}

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultLocaleName = kernel32.NewProc("GetUserDefaultLocaleName")
)
