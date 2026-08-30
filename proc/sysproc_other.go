//go:build !windows && !linux

package proc

import (
	"os/exec"
	"syscall"
)

func configureSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type lifetime struct{}

func newLifetime() *lifetime         { return &lifetime{} }
func (l *lifetime) attach(*exec.Cmd) {}
func (l *lifetime) release()         {}

func terminateTree(_ *lifetime, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
