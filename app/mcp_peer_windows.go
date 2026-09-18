package app

import (
	"errors"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// _WSAIOR(IOC_VENDOR, 256) from afunix.h; x/sys does not carry it.
const sioAFUnixGetPeerPID = 0x58000100

func mcpPeerPID(conn net.Conn) (int, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, errors.New("not a socket")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid uint32
	var ioErr error
	err = raw.Control(func(fd uintptr) {
		var n uint32
		ioErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	})
	if err != nil {
		return 0, err
	}
	if ioErr != nil {
		return 0, ioErr
	}
	return int(pid), nil
}

func readMCPProc(pid int) (mcpProcInfo, error) {
	var info mcpProcInfo
	ppid, err := windowsParentPID(uint32(pid))
	if err != nil {
		return info, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return info, err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return info, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return info, err
	}
	info.Path = windows.UTF16ToString(buf[:n])
	info.PPID = int(ppid)
	info.Start = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return info, nil
}

func windowsParentPID(pid uint32) (uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == pid {
			return e.ParentProcessID, nil
		}
	}
	return 0, errors.New("no such process")
}
