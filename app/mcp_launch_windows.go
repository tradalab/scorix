//go:build windows

package app

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// A new process group: the client's console signals must not reach the app.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}
