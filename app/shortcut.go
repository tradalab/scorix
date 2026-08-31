package app

import (
	"strings"

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
	parts := strings.Split(accel, "+")
	if len(parts) == 0 {
		return 0, 0, fault.New("invalid_shortcut", "empty accelerator")
	}
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl", "control":
			mods |= modControl
		case "alt":
			mods |= modAlt
		case "shift":
			mods |= modShift
		case "win", "super", "cmd", "meta":
			mods |= modWin
		default:
			return 0, 0, fault.Errorf("invalid_shortcut", "unknown modifier %q in %q", p, accel)
		}
	}
	key := strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	switch {
	case len(key) == 1 && (key[0] >= 'A' && key[0] <= 'Z' || key[0] >= '0' && key[0] <= '9'):
		vk = uint32(key[0])
	case strings.HasPrefix(key, "F") && len(key) >= 2 && len(key) <= 3:
		n := 0
		for _, c := range key[1:] {
			if c < '0' || c > '9' {
				return 0, 0, fault.Errorf("invalid_shortcut", "bad key %q in %q", key, accel)
			}
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > 24 {
			return 0, 0, fault.Errorf("invalid_shortcut", "F key out of range in %q", accel)
		}
		vk = uint32(0x70 + n - 1) // VK_F1
	default:
		named := map[string]uint32{
			"SPACE": 0x20, "TAB": 0x09, "ENTER": 0x0D, "RETURN": 0x0D,
			"ESC": 0x1B, "ESCAPE": 0x1B, "BACKSPACE": 0x08, "DELETE": 0x2E,
			"HOME": 0x24, "END": 0x23, "PAGEUP": 0x21, "PAGEDOWN": 0x22,
			"UP": 0x26, "DOWN": 0x28, "LEFT": 0x25, "RIGHT": 0x27,
		}
		v, ok := named[key]
		if !ok {
			return 0, 0, fault.Errorf("invalid_shortcut", "unknown key %q in %q", key, accel)
		}
		vk = v
	}
	if mods == 0 {
		return 0, 0, fault.Errorf("invalid_shortcut", "%q needs at least one modifier (a bare key would swallow normal typing system-wide)", accel)
	}
	return mods | modNoRepeat, vk, nil
}
