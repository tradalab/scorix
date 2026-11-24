//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func alive(pid int) bool {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false
		}
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func gone(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func groupedChild(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	child := exec.Command("/bin/sh", "-c", `sleep 60 & echo $! > "$1"; wait`, "child", pidFile)
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = child.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		<-exited
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			n, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			return child, n
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the child never started its grandchild")
	return nil, 0
}

func TestTheWatcherKillsTheChildsWholeGroupWhenTheParentDiesAbruptly(t *testing.T) {
	parent := exec.Command("sleep", "60")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	parentDone := make(chan struct{})
	go func() { _ = parent.Wait(); close(parentDone) }()
	child, grandchild := groupedChild(t)

	w, err := startWatcher(parent.Process.Pid, child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopWatcher(w) })

	time.Sleep(300 * time.Millisecond)
	if !alive(child.Process.Pid) || !alive(grandchild) {
		t.Fatal("the watcher killed the child while its parent was alive")
	}
	_ = parent.Process.Kill()
	<-parentDone

	if !gone(t, grandchild, 5*time.Second) {
		t.Fatal("the grandchild outlived the parent: the watcher did not kill the child's group")
	}
	if !gone(t, w.cmd.Process.Pid, 3*time.Second) {
		t.Fatal("the watcher kept running after it had nothing left to watch")
	}
}

func TestTheWatcherLeavesWhenTheChildExitsOnItsOwn(t *testing.T) {
	child := exec.Command("sleep", "0.3")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()

	w, err := startWatcher(os.Getpid(), child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopWatcher(w) })
	<-done
	if !gone(t, w.cmd.Process.Pid, 4*time.Second) {
		t.Fatal("the watcher outlived a child that exited by itself")
	}
}

func TestStoppingAWatcherThatAlreadyLeftSignalsNothing(t *testing.T) {
	child := exec.Command("true")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	w, err := startWatcher(os.Getpid(), child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.done:
	case <-time.After(4 * time.Second):
		t.Fatal("a watcher for a child already gone kept running")
	}
	if !w.finished() {
		t.Fatal("finished() disagrees with done")
	}
	stopWatcher(w)
	stopWatcher(nil)
}
