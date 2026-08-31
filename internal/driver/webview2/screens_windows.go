//go:build windows

package webview2

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/window"
)

var (
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")

	shcore               = windows.NewLazySystemDLL("shcore.dll")
	procGetDpiForMonitor = shcore.NewProc("GetDpiForMonitor")
	monitorEnumOnce      sync.Once
	monitorEnumCB        uintptr
	monitorEnumMu        sync.Mutex
	monitorEnumCollector *[]windows.Handle
	monitorInfoFPrimary  = uint32(1) // MONITORINFOF_PRIMARY
	mdtEffectiveDPI      = uintptr(0)
)

func (r *runtime) Screens() []window.Screen {
	monitorEnumOnce.Do(func() {
		monitorEnumCB = windows.NewCallback(func(hmon, _, _, _ uintptr) uintptr {
			if monitorEnumCollector != nil {
				*monitorEnumCollector = append(*monitorEnumCollector, windows.Handle(hmon))
			}
			return 1 // continue enumeration
		})
	})

	monitorEnumMu.Lock() // collector is a package var: a Go pointer laundered through LPARAM would trip checkptr
	var handles []windows.Handle
	monitorEnumCollector = &handles
	procEnumDisplayMonitors.Call(0, 0, monitorEnumCB, 0)
	monitorEnumCollector = nil
	monitorEnumMu.Unlock()

	screens := make([]window.Screen, 0, len(handles))
	for _, h := range handles {
		var mi monitorInfo
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(uintptr(h), uintptr(unsafe.Pointer(&mi))); r == 0 {
			continue
		}
		dpi := defaultDPI
		if procGetDpiForMonitor.Find() == nil {
			var dx, dy uint32
			if hr, _, _ := procGetDpiForMonitor.Call(uintptr(h), mdtEffectiveDPI,
				uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); hr == 0 && dx != 0 {
				dpi = int(dx)
			}
		}
		screens = append(screens, window.Screen{
			X:       toLogical(int(mi.rcMonitor.left), dpi),
			Y:       toLogical(int(mi.rcMonitor.top), dpi),
			W:       toLogical(int(mi.rcMonitor.right-mi.rcMonitor.left), dpi),
			H:       toLogical(int(mi.rcMonitor.bottom-mi.rcMonitor.top), dpi),
			Primary: mi.dwFlags&monitorInfoFPrimary != 0,
			Scale:   float64(dpi) / float64(defaultDPI),
		})
	}
	return screens
}
