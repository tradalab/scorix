//go:build !windows

package singleinstance

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func sockPath(name string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR") // per-user tmpfs on Linux desktops
	if dir == "" {
		dir = os.TempDir() // per-user $TMPDIR on macOS
	}
	return filepath.Join(dir, "scorix-"+name+".sock")
}

func Acquire(id string, onActivate func()) (*Lock, error) {
	name := sanitize(id)
	sock := sockPath(name)

	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		_, _ = c.Write([]byte("activate\n"))
		_ = c.Close()
		return nil, ErrAlreadyRunning
	}

	lockFile, err := os.OpenFile(sock+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if c, derr := net.Dial("unix", sock); derr == nil {
				_, _ = c.Write([]byte("activate\n"))
				_ = c.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return nil, ErrAlreadyRunning
	}

	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = lockFile.Close()
		return nil, err
	}

	stop := make(chan struct{})
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return // listener closed by Release
			}
			buf := make([]byte, 16)
			_, _ = c.Read(buf)
			_ = c.Close()
			select {
			case <-stop:
				return
			default:
			}
			if onActivate != nil {
				onActivate()
			}
		}
	}()

	return &Lock{release: func() {
		close(stop)
		_ = ln.Close()
		_ = os.Remove(sock)
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}}, nil
}
