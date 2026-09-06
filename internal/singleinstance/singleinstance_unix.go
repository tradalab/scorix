//go:build !windows

package singleinstance

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// sun_path holds 104 bytes on darwin and 108 on Linux, and a macOS $TMPDIR eats
// ~49 of them, so an over-long identifier turns Listen into an opaque
// "bind: invalid argument".
const maxSockPath = 100

func sockPath(name string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR") // per-user tmpfs on Linux desktops
	if dir == "" {
		dir = os.TempDir() // per-user $TMPDIR on macOS
	}
	p := filepath.Join(dir, "scorix-"+name+".sock")
	if len(p) <= maxSockPath {
		return p // readable: `ls $TMPDIR` should say which app holds the lock
	}
	// Hash, never truncate - two identifiers sharing a prefix must not share a lock.
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(dir, "scorix-"+hex.EncodeToString(sum[:8])+".sock")
}

func Acquire(id string, onActivate func(args []string)) (*Lock, error) {
	name := sanitize(id)
	sock := sockPath(name)

	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		_, _ = c.Write(payload())
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
				_, _ = c.Write(payload())
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
			buf := make([]byte, 64*1024)
			n, _ := c.Read(buf)
			_ = c.Close()
			select {
			case <-stop:
				return
			default:
			}
			if onActivate != nil {
				onActivate(parsePayload(buf[:n]))
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
