//go:build windows

package browser

import (
	"context"
	"os/exec"
	"syscall"
)

func openURL(_ context.Context, url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func reveal(path string) error {
	cmd := exec.Command("explorer.exe", "/select,"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func openPath(path string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
