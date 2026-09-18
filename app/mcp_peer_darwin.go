package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

func mcpPeerPID(conn net.Conn) (int, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, errors.New("not a socket")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var cred *unix.Xucred
	var optErr error
	if err := raw.Control(func(fd uintptr) {
		if pid, optErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); optErr != nil {
			return
		}
		cred, optErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if optErr != nil {
		return 0, optErr
	}
	if int(cred.Uid) != os.Getuid() {
		return 0, fmt.Errorf("the connecting process belongs to uid %d", cred.Uid)
	}
	return pid, nil
}

var (
	procPidpathOnce sync.Once
	procPidpath     func(pid int32, buf *byte, size uint32) int32
	procPidpathErr  error
)

// proc_pidpath, not kern.procargs2: that one keeps the path as exec got it, relative
// when a shell ran ./tool. Dlsym, so a missing symbol refuses rather than panics.
func loadProcPidpath() error {
	procPidpathOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			procPidpathErr = err
			return
		}
		sym, err := purego.Dlsym(lib, "proc_pidpath")
		if err != nil {
			procPidpathErr = err
			return
		}
		purego.RegisterFunc(&procPidpath, sym)
	})
	return procPidpathErr
}

func readMCPProc(pid int) (mcpProcInfo, error) {
	var info mcpProcInfo
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return info, err
	}
	if err := loadProcPidpath(); err != nil {
		return info, err
	}
	buf := make([]byte, 4096) // PROC_PIDPATHINFO_MAXSIZE
	n := procPidpath(int32(pid), &buf[0], uint32(len(buf)))
	if n <= 0 {
		return info, fmt.Errorf("proc_pidpath(%d) failed", pid)
	}
	info.Path = string(buf[:n])
	info.PPID = int(k.Eproc.Ppid)
	info.Start = uint64(k.Proc.P_starttime.Sec)*1_000_000 + uint64(k.Proc.P_starttime.Usec)
	return info, nil
}
