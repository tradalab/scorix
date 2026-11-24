//go:build !windows

package proc

import (
	"os/exec"
	"strconv"
	"syscall"
)

const watchScript = `p=$1 c=$2
while kill -0 "$p" 2>/dev/null && kill -0 "$c" 2>/dev/null; do sleep 1; done
kill -0 "$p" 2>/dev/null || kill -KILL -"$c" 2>/dev/null || kill -KILL "$c" 2>/dev/null
exit 0`

type watcher struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func startWatcher(parent, child int) (*watcher, error) {
	cmd := exec.Command("/bin/sh", "-c", watchScript, "scorix-proc-watch", strconv.Itoa(parent), strconv.Itoa(child))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &watcher{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(w.done)
	}()
	return w, nil
}

func (w *watcher) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func stopWatcher(w *watcher) {
	if w == nil || w.finished() {
		return
	}
	_ = w.cmd.Process.Kill()
}
