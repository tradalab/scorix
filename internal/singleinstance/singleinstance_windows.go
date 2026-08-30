//go:build windows

package singleinstance

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const pipePrefix = `\\.\pipe\scorix.`

func Acquire(id string, onActivate func()) (*Lock, error) {
	name := sanitize(id)
	mutexName, err := windows.UTF16PtrFromString(`Local\scorix.` + name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, mutexName)
	if h != 0 && err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		notify(name)
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, err
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go serve(name, onActivate, stop, done)
	return &Lock{release: func() {
		close(stop)
		deadline := time.Now().Add(time.Second)
		for {
			if f, err := os.OpenFile(pipePrefix+name, os.O_WRONLY, 0); err == nil {
				_ = f.Close()
			}
			select {
			case <-done:
				_ = windows.CloseHandle(h)
				return
			case <-time.After(20 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				_ = windows.CloseHandle(h) // give up waiting; the mutex still frees
				return
			}
		}
	}}, nil
}

func serve(name string, onActivate func(), stop, done chan struct{}) {
	defer close(done)
	pipeName, err := windows.UTF16PtrFromString(pipePrefix + name)
	if err != nil {
		return
	}
	for {
		select {
		case <-stop:
			return
		default:
		}
		h, err := windows.CreateNamedPipe(pipeName,
			windows.PIPE_ACCESS_INBOUND,
			windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT,
			windows.PIPE_UNLIMITED_INSTANCES, 512, 512, 0, nil)
		if err != nil {
			return
		}
		cerr := windows.ConnectNamedPipe(h, nil)
		select {
		case <-stop:
			_ = windows.CloseHandle(h)
			return
		default:
		}
		if cerr != nil && cerr != windows.ERROR_PIPE_CONNECTED {
			_ = windows.CloseHandle(h)
			continue
		}
		var n uint32
		buf := make([]byte, 16)
		_ = windows.ReadFile(h, buf, &n, nil)
		_ = windows.CloseHandle(h)
		if onActivate != nil {
			onActivate()
		}
	}
}

func notify(name string) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, err := os.OpenFile(pipePrefix+name, os.O_WRONLY, 0)
		if err == nil {
			_, _ = f.Write([]byte("activate\n"))
			_ = f.Close()
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
