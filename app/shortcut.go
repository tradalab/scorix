package app

import (
	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/window"
)

type shortcut struct {
	accel string
	mods  uint32
	vk    uint32
	fn    func()
}

func (a *App) OnGlobalShortcut(accel string, fn func()) error {
	mods, vk, err := parseAccelerator(accel)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.shortcuts = append(a.shortcuts, shortcut{accel: accel, mods: mods, vk: vk, fn: fn})
	a.mu.Unlock()
	return nil
}

func (a *App) registerShortcuts(rt window.Runtime) {
	a.mu.Lock()
	list := append([]shortcut{}, a.shortcuts...)
	a.mu.Unlock()
	if len(list) == 0 {
		return
	}
	reg, ok := rt.(window.HotkeyRegistrar)
	if !ok {
		logger.Warn("app: global shortcuts are not supported by this driver", "count", len(list))
		return
	}
	for i, sc := range list {
		sc := sc
		err := reg.RegisterHotkey(i+1, sc.mods, sc.vk, func() {
			go func() {
				sc.fn()
				a.Emit("sys:shortcut", map[string]string{"accel": sc.accel})
			}()
		})
		if err != nil {
			logger.Warn("app: global shortcut rejected by the OS", "accel", sc.accel, "err", err)
		}
	}
}

const (
	modAlt      = 0x1
	modControl  = 0x2
	modShift    = 0x4
	modWin      = 0x8
	modNoRepeat = 0x4000
)

func parseAccelerator(accel string) (mods uint32, vk uint32, err error) {
	acc, err := window.ParseAccel(accel)
	if err != nil {
		return 0, 0, fault.Wrap("invalid_shortcut", err)
	}
	mods, vk = acc.Win32()
	if mods == 0 {
		return 0, 0, fault.Errorf("invalid_shortcut", "%q needs at least one modifier (a bare key would swallow normal typing system-wide)", accel)
	}
	return mods | modNoRepeat, vk, nil
}
