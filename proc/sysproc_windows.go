//go:build windows

package proc

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

type lifetime struct {
	job windows.Handle
}

func newLifetime() *lifetime {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return &lifetime{} // best-effort: supervision still works, orphan net doesn't
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, _ = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return &lifetime{job: h}
}

func (l *lifetime) attach(cmd *exec.Cmd) {
	if l == nil || l.job == 0 || cmd.Process == nil {
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	_ = windows.AssignProcessToJobObject(l.job, ph)
	_ = windows.CloseHandle(ph)
}

func (l *lifetime) release() {
	if l != nil && l.job != 0 {
		_ = windows.CloseHandle(l.job) // KILL_ON_JOB_CLOSE reaps anything still inside
		l.job = 0
	}
}

func terminateTree(l *lifetime, cmd *exec.Cmd) {
	if l != nil && l.job != 0 {
		_ = windows.TerminateJobObject(l.job, 1)
		return
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
