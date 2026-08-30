package app

import (
	"context"
	"encoding/json"

	"github.com/tradalab/scorix/fault"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/window"
)

func (a *App) registerWindowCommands() {
	reg := func(name string, fn func(w *AppWindow, data json.RawMessage) (any, error)) {
		a.reg.Command("win:"+name, func(ctx context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
			w := a.windowFrom(ctx)
			if w == nil {
				return nil, fault.New(fault.CodeUnavailable, "win: no native window for this client (app mode only)")
			}
			return fn(w, data)
		})
	}

	reg("minimize", func(w *AppWindow, _ json.RawMessage) (any, error) {
		w.Minimize()
		return nil, nil
	})
	reg("toggleMaximize", func(w *AppWindow, _ json.RawMessage) (any, error) {
		maximized := w.State() == window.StateMaximized
		if maximized {
			w.Unmaximize()
		} else {
			w.Maximize()
		}
		return map[string]bool{"maximized": !maximized}, nil
	})
	reg("isMaximized", func(w *AppWindow, _ json.RawMessage) (any, error) {
		return map[string]bool{"maximized": w.State() == window.StateMaximized}, nil
	})
	reg("close", func(w *AppWindow, _ json.RawMessage) (any, error) {
		w.Close()
		return nil, nil
	})
	reg("hide", func(w *AppWindow, _ json.RawMessage) (any, error) {
		w.Hide()
		return nil, nil
	})
	reg("show", func(w *AppWindow, _ json.RawMessage) (any, error) {
		w.Show()
		return nil, nil
	})
	reg("focus", func(w *AppWindow, _ json.RawMessage) (any, error) {
		w.Focus()
		return nil, nil
	})
	reg("setTitle", func(w *AppWindow, data json.RawMessage) (any, error) {
		var req struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, fault.Wrap("invalid_request", err)
		}
		w.SetTitle(req.Title)
		return nil, nil
	})
	reg("fullscreen", func(w *AppWindow, data json.RawMessage) (any, error) {
		var req struct {
			On bool `json:"on"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, fault.Wrap("invalid_request", err)
		}
		w.SetFullscreen(req.On)
		return nil, nil
	})
	reg("startDrag", func(w *AppWindow, _ json.RawMessage) (any, error) {
		w.StartDrag()
		return nil, nil
	})
}

func (a *App) windowFrom(ctx context.Context) *AppWindow {
	id, ok := ClientFrom(ctx)
	if !ok {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wins[id]
}
