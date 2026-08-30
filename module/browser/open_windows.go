//go:build windows

package browser

import (
	"os/exec"
	"syscall"
)

func revealCmd(path string) *exec.Cmd {
	cmd := exec.Command("explorer.exe", "/select,"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func openPathCmd(path string) *exec.Cmd {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
