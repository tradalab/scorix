package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

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
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if int(cred.Uid) != os.Getuid() {
		return 0, fmt.Errorf("the connecting process belongs to uid %d", cred.Uid)
	}
	return int(cred.Pid), nil
}

func readMCPProc(pid int) (mcpProcInfo, error) {
	var info mcpProcInfo
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return info, err
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return info, err
	}
	// The command in parentheses may hold spaces and parentheses: count from the last ')'.
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return info, errors.New("unreadable /proc stat")
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 {
		return info, errors.New("unreadable /proc stat")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return info, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return info, err
	}
	// A client updated while it runs keeps its approval: the path is the same program.
	info.Path = strings.TrimSuffix(exe, " (deleted)")
	info.PPID = ppid
	info.Start = start
	return info, nil
}
