//go:build !windows

package app

import "github.com/tradalab/scorix/fault"

func (a *App) SetAutostart(bool) error {
	return fault.New(fault.CodeUnavailable, "autostart is only implemented on Windows")
}

func (a *App) AutostartEnabled() (bool, error) {
	return false, fault.New(fault.CodeUnavailable, "autostart is only implemented on Windows")
}
