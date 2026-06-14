//go:build linux

package webkitgtk

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

type manager struct {
	rt *rt

	mu   sync.Mutex
	byID map[window.ID]*win
	seq  int
}

var (
	winByWidget sync.Map // GtkWindow ptr -> *win
	winSignals  sync.Once
	destroyCB   uintptr
	deleteCB    uintptr
)

func initWinSignals() {
	winSignals.Do(func() {
		destroyCB = purego.NewCallback(func(widget, _ uintptr) uintptr {
			if v, ok := winByWidget.Load(widget); ok {
				w := v.(*win)
				winByWidget.Delete(widget)
				w.fire(window.EventClose)
				if w.rt.manager.remove(w.id) == 0 {
					gtkMainQuit()
				}
			}
			return 0
		})
		// "delete-event": close button pressed — TRUE swallows the close
		// (hide-on-close), FALSE lets GTK destroy the window.
		deleteCB = purego.NewCallback(func(widget, _, _ uintptr) uintptr {
			if v, ok := winByWidget.Load(widget); ok {
				w := v.(*win)
				w.mu.Lock()
				hide := w.hideOnClose
				w.mu.Unlock()
				if hide {
					gtkWidgetHide(widget)
					return 1
				}
			}
			return 0
		})
	})
}

// New must run on the UI thread.
func (m *manager) New(opts window.Options) (window.Window, error) {
	initWinSignals()

	if opts.Width == 0 {
		opts.Width = 800
	}
	if opts.Height == 0 {
		opts.Height = 600
	}

	gw := gtkWindowNew(gtkWindowToplevel)
	if gw == 0 {
		return nil, fmt.Errorf("webkitgtk: gtk_window_new failed")
	}
	if opts.Title != "" {
		gtkWindowSetTitle(gw, opts.Title)
	}
	gtkWindowSetDefault(gw, int32(opts.Width), int32(opts.Height))
	if opts.Frameless {
		gtkWindowSetDecor(gw, 0)
	}
	if !opts.Resizable {
		gtkWindowSetResize(gw, 0)
	}
	if opts.X != nil && opts.Y != nil {
		gtkWindowMove(gw, int32(*opts.X), int32(*opts.Y))
	}

	v, err := newView(m.rt, opts)
	if err != nil {
		return nil, err
	}
	gtkContainerAdd(gw, v.wk)

	m.mu.Lock()
	id := opts.ID
	if id == "" {
		m.seq++
		id = window.ID("win-" + strconv.Itoa(m.seq))
	}
	w := &win{
		id:          id,
		gw:          gw,
		rt:          m.rt,
		view:        v,
		hideOnClose: opts.HideOnClose,
		events:      map[window.Event][]func(window.EventData){},
	}
	m.byID[id] = w
	m.mu.Unlock()

	winByWidget.Store(gw, w)
	signalConnect(gw, "destroy", destroyCB, 0)
	signalConnect(gw, "delete-event", deleteCB, 0)

	if opts.URL != "" {
		v.Navigate(opts.URL)
	}
	return w, nil
}

func (m *manager) Get(id window.ID) (window.Window, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.byID[id]
	return w, ok
}

func (m *manager) All() []window.Window {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]window.Window, 0, len(m.byID))
	for _, w := range m.byID {
		out = append(out, w)
	}
	return out
}

func (m *manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byID)
}

func (m *manager) remove(id window.ID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, id)
	return len(m.byID)
}

type win struct {
	id          window.ID
	gw          uintptr // GtkWindow*
	rt          *rt
	view        *view
	hideOnClose bool

	mu     sync.Mutex
	events map[window.Event][]func(window.EventData)
}

func (w *win) ID() window.ID      { return w.id }
func (w *win) View() webview.View { return w.view }

func (w *win) SetTitle(t string) { dispatchMain(func() { gtkWindowSetTitle(w.gw, t) }) }

func (w *win) SetSize(width, height int) {
	dispatchMain(func() { gtkWindowResize(w.gw, int32(width), int32(height)) })
}

func (w *win) Size() (int, int) {
	type sz struct{ w, h int32 }
	ch := make(chan sz, 1)
	dispatchMain(func() {
		var a, b int32
		gtkWindowGetSize(w.gw, &a, &b)
		ch <- sz{a, b}
	})
	s := <-ch
	return int(s.w), int(s.h)
}

func (w *win) SetPosition(x, y int) {
	dispatchMain(func() { gtkWindowMove(w.gw, int32(x), int32(y)) })
}

func (w *win) Position() (int, int) {
	type pt struct{ x, y int32 }
	ch := make(chan pt, 1)
	dispatchMain(func() {
		var a, b int32
		gtkWindowGetPosition(w.gw, &a, &b)
		ch <- pt{a, b}
	})
	p := <-ch
	return int(p.x), int(p.y)
}

func (w *win) SetMinSize(int, int) {} // TODO: gtk_widget_set_size_request / geometry hints
func (w *win) SetMaxSize(int, int) {} // TODO: GdkGeometry hints

func (w *win) Center() {} // GTK3 centers via gtk_window_set_position before show — TODO

func (w *win) Show() { dispatchMain(func() { gtkWidgetShowAll(w.gw); gtkWindowPresent(w.gw) }) }
func (w *win) Hide() { dispatchMain(func() { gtkWidgetHide(w.gw) }) }

func (w *win) Focus() { dispatchMain(func() { gtkWindowPresent(w.gw) }) }

func (w *win) Minimize()   { dispatchMain(func() { gtkWindowIconify(w.gw) }) }
func (w *win) Maximize()   { dispatchMain(func() { gtkWindowMaximize(w.gw) }) }
func (w *win) Unmaximize() { dispatchMain(func() { gtkWindowUnmaximize(w.gw) }) }
func (w *win) Restore()    { dispatchMain(func() { gtkWindowDeiconify(w.gw) }) }

func (w *win) SetFullscreen(on bool) {
	dispatchMain(func() {
		if on {
			gtkWindowFullscreen(w.gw)
			return
		}
		gtkWindowUnfullscrn(w.gw)
	})
}

func (w *win) SetAlwaysOnTop(on bool) {
	v := int32(0)
	if on {
		v = 1
	}
	dispatchMain(func() { gtkWindowKeepAbove(w.gw, v) })
}

func (w *win) IsVisible() bool     { return true }              // TODO: gtk_widget_get_visible
func (w *win) State() window.State { return window.StateNormal } // TODO

func (w *win) Close() { dispatchMain(func() { gtkWidgetDestroy(w.gw) }) }

func (w *win) SetHideOnClose(on bool) {
	w.mu.Lock()
	w.hideOnClose = on
	w.mu.Unlock()
}

func (w *win) On(evt window.Event, fn func(window.EventData)) {
	w.mu.Lock()
	w.events[evt] = append(w.events[evt], fn)
	w.mu.Unlock()
}

func (w *win) fire(evt window.Event) {
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[evt]...)
	w.mu.Unlock()
	data := window.EventData{Window: w.id}
	for _, fn := range fns {
		fn(data)
	}
}
