//go:build !windows

package app

import "syscall"

// A new session, so the app outlives the client that started it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
