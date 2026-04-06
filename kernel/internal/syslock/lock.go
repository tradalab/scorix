package syslock

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/logger"
)

// Acquire attempts to acquire a single-instance lock.
// Returns true if primary, false if secondary (secondary sends FOCUS to primary).
func Acquire(identifier string, onFocus func()) bool {
	if identifier == "" {
		identifier = "scorix-default"
	}

	hash := md5.Sum([]byte(identifier))
	lockFileName := fmt.Sprintf("scorix_lock_%s.sockport", hex.EncodeToString(hash[:]))
	lockPath := filepath.Join(os.TempDir(), lockFileName)

	port := readPortFromLockfile(lockPath)
	if port > 0 {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_, _ = conn.Write([]byte("FOCUS\n"))
			conn.Close()
			logger.Info("SingleInstance: Another instance found, FOCUS sent")
			return false
		}
		_ = os.Remove(lockPath)
	}

	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		logger.Error(fmt.Sprintf("SingleInstance: listen error: %v", err))
		return true
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(lockPath, []byte(strconv.Itoa(actualPort)), 0600)

	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				continue
			}

			buf := make([]byte, 32)
			n, _ := conn.Read(buf)
			cmd := strings.TrimSpace(string(buf[:n]))
			conn.Close()

			if cmd == "FOCUS" && onFocus != nil {
				onFocus()
			}
		}
	}()

	return true
}

// readPortFromLockfile reads the port number from the lockfile. Returns 0 if invalid/missing.
func readPortFromLockfile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return port
}
