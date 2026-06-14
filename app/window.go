package app

import (
	"errors"

	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/window"
)

type AppWindow struct {
	window.Window
	Client ClientID // targeted-emit id for this window's frontend
}

// OpenWindow opens an additional native window with the bridge injected and IPC
// wired; per-window traffic uses the returned Client with EmitTo. Zero
// Width/Height/URL fall back to the main-window options.
//
// App mode only, after Run has started. It blocks on the UI thread, so don't
// call it from code already on it (OnReady, window event handlers); wrap those
// in a goroutine. The loop exits when the last window closes.
func (a *App) OpenWindow(opts window.Options) (*AppWindow, error) {
	a.mu.Lock()
	rt := a.rt
	a.mu.Unlock()
	if rt == nil {
		return nil, errors.New("scorix: OpenWindow requires app mode with a running runtime (call after Run has started)")
	}

	if opts.Width == 0 {
		opts.Width = a.opts.Width
	}
	if opts.Height == 0 {
		opts.Height = a.opts.Height
	}
	if opts.URL == "" {
		opts.URL = a.opts.URL
	}

	type result struct {
		w   *AppWindow
		err error
	}
	ch := make(chan result, 1)
	rt.Dispatch(func() { // Manager.New must run on the UI thread
		aw, err := a.attachWindow(rt, opts)
		if err == nil {
			aw.Show()
		}
		ch <- result{aw, err}
	})
	r := <-ch
	return r.w, r.err
}

// Quit stops the native event loop (app mode); no-op if none is running.
func (a *App) Quit() {
	a.mu.Lock()
	rt := a.rt
	a.mu.Unlock()
	if rt != nil {
		rt.Quit()
	}
}

// attachWindow creates a window, injects the bridge, and wires its IPC
// dispatcher to a fresh ClientID. MUST run on the UI thread (Manager.New contract).
func (a *App) attachWindow(rt window.Runtime, opts window.Options) (*AppWindow, error) {
	// Bridge runs before any page script; a caller InitScript runs after it.
	if opts.InitScript == "" {
		opts.InitScript = bridgeJS
	} else {
		opts.InitScript = bridgeJS + "\n;" + opts.InitScript
	}

	w, err := rt.Windows().New(opts)
	if err != nil {
		return nil, err
	}
	b := ipc.NewNativeBridge(w.View(), a.reg)
	sid := a.addSender(func(raw []byte) { _ = w.View().PostMessage(raw) })
	b.BindClient(ipc.ClientID(sid))
	a.mu.Lock()
	a.bridges = append(a.bridges, b)
	a.mu.Unlock()
	// Stop broadcasting to a disposed view once the window closes.
	w.On(window.EventClose, func(window.EventData) { a.removeSender(sid) })
	return &AppWindow{Window: w, Client: ipc.ClientID(sid)}, nil
}
