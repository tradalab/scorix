//go:build windows

package app

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func (a *App) SetAutostart(on bool) error {
	name := a.autostartName()
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !on {
		err := key.DeleteValue(name)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(name, `"`+exe+`"`)
}

func (a *App) AutostartEnabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
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
