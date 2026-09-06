//go:build windows

package app

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// var, not const: the absent-key path is untestable where the real key exists.
var runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// HKCU\...\Run is created on demand: a profile that never registered a startup
// entry has no such key, and ERROR_FILE_NOT_FOUND there means absent, not broken.
func (a *App) SetAutostart(on bool) error {
	name := a.autostartName()
	if !on {
		key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if err == registry.ErrNotExist {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		if err := key.DeleteValue(name); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(name, `"`+exe+`"`)
}

func (a *App) AutostartEnabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	if _, _, err := key.GetStringValue(a.autostartName()); err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *App) autostartName() string {
	if a.opts.Identifier != "" {
		return a.opts.Identifier
	}
	return "scorix-app"
}
