//go:build !windows && !linux

package proc

import (
	"os/exec"
	"sync"
	"syscall"
)

const orphanNet = "watcher"

func configureSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type lifetime struct {
	mu       sync.Mutex
	watchers []*watcher
}

func newLifetime() *lifetime { return &lifetime{} }

func (l *lifetime) attach(cmd *exec.Cmd) {
	if l == nil || cmd == nil || cmd.Process == nil {
		return
	}
	w, err := startWatcher(syscall.Getpid(), cmd.Process.Pid)
	if err != nil {
		return
	}
	l.mu.Lock()
	live := l.watchers[:0]
	for _, old := range l.watchers {
		if !old.finished() {
			live = append(live, old)
		}
	}
	l.watchers = append(live, w)
	l.mu.Unlock()
}

func (l *lifetime) release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	ws := l.watchers
	l.watchers = nil
	l.mu.Unlock()
	for _, w := range ws {
		stopWatcher(w)
	}
}

func terminateTree(_ *lifetime, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
