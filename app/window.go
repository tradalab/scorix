package app

import (
	"context"
	"errors"
	"sync"
	"time"

	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/window"
)

type AppWindow struct {
	window.Window
	Client ClientID // targeted-emit id for this window's frontend
}

// OpenWindow opens an additional native window (bridge injected, IPC wired); zero
// Width/Height/URL fall back to main-window options. App mode only, after Run.
// Blocks on the UI thread — don't call from code already on it (OnReady, window
// event handlers); wrap those in a goroutine.
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

// Quit stops the native event loop (app mode); no-op if none running.
func (a *App) Quit() {
	a.mu.Lock()
	rt := a.rt
	a.mu.Unlock()
	if rt != nil {
		rt.Quit()
	}
}

// MUST run on the UI thread (Manager.New contract).
func (a *App) attachWindow(rt window.Runtime, opts window.Options) (*AppWindow, error) {
	// Bridge runs before any page script; a caller InitScript runs after.
	if opts.InitScript == "" {
		opts.InitScript = bridgeJS
	} else {
		opts.InitScript = bridgeJS + "\n;" + opts.InitScript
	}

	w, err := rt.Windows().New(opts)
	if err != nil {
		return nil, err
	}
	view := w.View()
	// One serialized writer per view (RPC replies + broadcast/EmitTo): native
	// PostMessage isn't assumed concurrency-safe, so Emit must not interleave a Send.
	var wmu sync.Mutex
	send := func(raw []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return view.PostMessage(raw)
	}
	d := ipc.NewDispatcher(a.reg, send)
	view.OnMessage(d.Handle)
	b := &ipc.NativeBridge{Dispatcher: d}
	sid := a.addSender(func(raw []byte) { _ = send(raw) })
	b.BindClient(ipc.ClientID(sid))
	a.mu.Lock()
	a.bridges = append(a.bridges, b)
	a.mu.Unlock()
	// On close, cancel the bridge's handlers too: else a long-lived stream (monitor,
	// pubsub) leaks a goroutine per closed window — native PostMessage can't report
	// the gone client.
	w.On(window.EventClose, func(window.EventData) {
		a.removeSender(sid)
		a.mu.Lock()
		for i, x := range a.bridges {
			if x == b {
				a.bridges = append(a.bridges[:i], a.bridges[i+1:]...)
				break
			}
		}
		a.mu.Unlock()
		// winClose lets Run's teardown wait for this drain before stopping modules.
		a.winClose.Add(1)
		go func() {
			defer a.winClose.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.Close(ctx)
		}()
	})
	return &AppWindow{Window: w, Client: ipc.ClientID(sid)}, nil
}
