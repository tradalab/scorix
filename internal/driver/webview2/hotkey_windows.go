//go:build windows

package webview2

import (
	"fmt"
)

var procRegisterHotKey = user32.NewProc("RegisterHotKey")

func (r *runtime) RegisterHotkey(id int, mods, vk uint32, fn func()) error {
	r.mu.Lock()
	hwnd := r.msgHWND
	if r.hotkeys == nil {
		r.hotkeys = map[uintptr]func(){}
	}
	r.mu.Unlock()
	if hwnd == 0 {
		return fmt.Errorf("hotkey: runtime not started")
	}

	errCh := make(chan error, 1)
	r.Dispatch(func() { // RegisterHotKey binds to the calling thread's queue: must run inside the loop
		ok, _, lastErr := procRegisterHotKey.Call(uintptr(hwnd), uintptr(id), uintptr(mods), uintptr(vk))
		if ok == 0 {
			errCh <- fmt.Errorf("RegisterHotKey(%d): %w", id, lastErr)
			return
		}
		r.mu.Lock()
		r.hotkeys[uintptr(id)] = fn
		r.mu.Unlock()
		errCh <- nil
	})
	return <-errCh
}

func (r *runtime) fireHotkey(id uintptr) {
	r.mu.Lock()
	fn := r.hotkeys[id]
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}
