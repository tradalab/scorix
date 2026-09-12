//go:build linux

package webkitgtk

import (
	"fmt"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/tradalab/scorix/logger"
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
			defer recoverCB("window-destroy")
			if v, ok := winByWidget.Load(widget); ok {
				w := v.(*win)
				winByWidget.Delete(widget)
				viewByUcm.Delete(w.view.ucm)
				w.releaseMenuRefs()
				w.mu.Lock()
				fired := w.closeFired
				w.mu.Unlock()
				if !fired {
					w.fire(window.EventClose)
				}
				if w.rt.manager.remove(w.id) == 0 {
					gtkMainQuit()
				}
			}
			return 0
		})
		// "delete-event": close button pressed - TRUE swallows the close
		// (hide-on-close), FALSE lets GTK destroy the window.
		deleteCB = purego.NewCallback(func(widget, _, _ uintptr) uintptr {
			defer recoverCB("window-delete-event")
			if v, ok := winByWidget.Load(widget); ok {
				w := v.(*win)
				w.mu.Lock()
				hide := w.hideOnClose
				w.mu.Unlock()
				if hide {
					gtkWidgetHide(widget)
					return 1 // TRUE: swallow the close
				}
				if w.fireClose() { // EventClose handler called PreventDefault
					return 1
				}
			}
			return 0 // FALSE: let GTK destroy the window
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
	// Options has carried these four since the beginning and only webview2 read
	// them, so a min_width in scorix.yaml did nothing here and said nothing about
	// it. Declared at creation, the window manager honours them from the first map.
	if opts.MinWidth > 0 || opts.MinHeight > 0 {
		minW, minH := int32(-1), int32(-1) // -1 is GTK's own "no request"
		if opts.MinWidth > 0 {
			minW = int32(opts.MinWidth)
		}
		if opts.MinHeight > 0 {
			minH = int32(opts.MinHeight)
		}
		gtkWidgetSetSizeReq(gw, minW, minH)
	}
	if opts.MaxWidth > 0 || opts.MaxHeight > 0 {
		// GDK reads 0 as "cannot grow at all", not as "unlimited", so an axis the
		// app left open gets a cap no display reaches instead of a cap of zero.
		maxW, maxH := int32(gdkSizeUnlimited), int32(gdkSizeUnlimited)
		if opts.MaxWidth > 0 {
			maxW = int32(opts.MaxWidth)
		}
		if opts.MaxHeight > 0 {
			maxH = int32(opts.MaxHeight)
		}
		g := gdkGeometry{maxWidth: maxW, maxHeight: maxH}
		gtkWindowSetGeomHints(gw, 0, unsafe.Pointer(&g), gdkHintMaxSize)
	}
	if opts.Frameless {
		gtkWindowSetDecor(gw, 0)
	}
	if !opts.Resizable {
		gtkWindowSetResize(gw, 0)
	}
	if opts.X != nil && opts.Y != nil {
		gtkWindowMove(gw, int32(*opts.X), int32(*opts.Y))
	}
	if opts.AlwaysOnTop {
		gtkWindowKeepAbove(gw, 1)
	}
	if opts.IconPath != "" {
		warnWindowIconIgnored()
	}

	v, err := newView(m.rt, opts)
	if err != nil {
		return nil, err
	}
	vbox := gtkBoxNew(gtkOrientationVertical, 0)
	gtkContainerAdd(gw, vbox)
	gtkBoxPackStart(vbox, v.wk, 1, 1, 0)

	m.mu.Lock()
	id := opts.ID
	if id == "" {
		m.seq++
		id = window.ID("win-" + strconv.Itoa(m.seq))
	}
	w := &win{
		id:          id,
		gw:          gw,
		vbox:        vbox,
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

// A cap on one axis needs a value for the other, and GDK has no "unlimited":
// 16.7M pixels is past every display while staying inside int32.
const gdkSizeUnlimited = 1 << 24

var warnIconOnce sync.Once

// Once per process: an app that opens many windows would otherwise repeat it.
// Wiring gtk_window_set_icon_from_file would mean one more purego symbol, and a
// misspelled one panics at runtime while build, vet and tests all stay green.
func warnWindowIconIgnored() {
	warnIconOnce.Do(func() {
		logger.Warn("webkitgtk: app.icon is not a window icon on Linux - a launcher reads Icon= from the .desktop entry, which `scorix package` writes and checks")
	})
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
	vbox        uintptr // GtkBox: a menu bar packs above the webview
	rt          *rt
	view        *view
	hideOnClose bool

	mu         sync.Mutex
	events     map[window.Event][]func(window.EventData)
	closeFired bool // EventClose already fired (delete-event); destroy won't re-fire
	menubar    uintptr
	accelGroup uintptr
	menuIDs    []uintptr
}

func (w *win) fireClose() (prevented bool) {
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[window.EventClose]...)
	id := w.id
	w.mu.Unlock()
	data := window.EventData{Window: id, PreventDefault: func() { prevented = true }}
	for _, fn := range fns {
		fn(data)
	}
	if !prevented {
		w.mu.Lock()
		w.closeFired = true
		w.mu.Unlock()
	}
	return prevented
}

func (w *win) ID() window.ID      { return w.id }
func (w *win) View() webview.View { return w.view }

func (w *win) SetTitle(t string) { dispatchMain(func() { gtkWindowSetTitle(w.gw, t) }) }

func (w *win) SetSize(width, height int) {
	dispatchMain(func() { gtkWindowResize(w.gw, int32(width), int32(height)) })
}

func (w *win) Size() (int, int) {
	s := onMainVal(func() [2]int32 {
		var a, b int32
		gtkWindowGetSize(w.gw, &a, &b)
		return [2]int32{a, b}
	})
	return int(s[0]), int(s[1])
}

func (w *win) SetPosition(x, y int) {
	dispatchMain(func() { gtkWindowMove(w.gw, int32(x), int32(y)) })
}

func (w *win) Position() (int, int) {
	p := onMainVal(func() [2]int32 {
		var a, b int32
		gtkWindowGetPosition(w.gw, &a, &b)
		return [2]int32{a, b}
	})
	return int(p[0]), int(p[1])
}

func (w *win) SetMinSize(width, height int) {
	dispatchMain(func() { gtkWidgetSetSizeReq(w.gw, int32(width), int32(height)) })
}

// gdkGeometry mirrors GdkGeometry: 8 gint, then 2 gdouble, then GdkGravity. Go
// lays these out identically on 64-bit, so the struct goes over as-is.
type gdkGeometry struct {
	minWidth, minHeight   int32
	maxWidth, maxHeight   int32
	baseWidth, baseHeight int32
	widthInc, heightInc   int32
	minAspect, maxAspect  float64
	winGravity            int32
	_                     int32
}

// Only the MAX bit, so the minimum stays with gtk_widget_set_size_request where
// SetMinSize put it. g is captured, so the pointer is still valid when the loop
// runs the task.
func (w *win) SetMaxSize(width, height int) {
	g := gdkGeometry{maxWidth: int32(width), maxHeight: int32(height)}
	dispatchMain(func() { gtkWindowSetGeomHints(w.gw, 0, unsafe.Pointer(&g), gdkHintMaxSize) })
}

func (w *win) Center() { dispatchMain(func() { gtkWindowSetPosition(w.gw, 1) }) }

func (w *win) Show() { dispatchMain(func() { gtkWidgetShowAll(w.gw); gtkWindowPresent(w.gw) }) }
func (w *win) Hide() { dispatchMain(func() { gtkWidgetHide(w.gw) }) }

func (w *win) Focus() { dispatchMain(func() { gtkWindowPresent(w.gw) }) }

func (w *win) Minimize()   { dispatchMain(func() { gtkWindowIconify(w.gw) }) }
func (w *win) Maximize()   { dispatchMain(func() { gtkWindowMaximize(w.gw) }) }
func (w *win) Unmaximize() { dispatchMain(func() { gtkWindowUnmaximize(w.gw) }) }
func (w *win) Restore()    { dispatchMain(func() { gtkWindowDeiconify(w.gw) }) }

// Asks the pointer where it is rather than the event: startDrag arrives over
// IPC, so GTK's current event is long gone by the time this reaches the loop and
// its timestamp would be 0 anyway.
func (w *win) StartDrag() {
	dispatchMain(func() {
		pointer := gdkSeatGetPointer(gdkDisplayDefaultSeat(gdkDisplayGetDefault()))
		if pointer == 0 {
			return
		}
		var screen uintptr
		var x, y int32
		gdkDeviceGetPosition(pointer, &screen, &x, &y)
		gtkWindowBeginMove(w.gw, 1, x, y, 0) // button 1, GDK_CURRENT_TIME
	})
}

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

func (w *win) IsVisible() bool {
	return onMainVal(func() bool { return gtkWidgetGetVisible(w.gw) != 0 })
}

// Asked of GDK, not tracked, so a change the USER made is still reported.
func (w *win) State() window.State {
	return onMainVal(func() window.State {
		gw := gtkWidgetGetWindow(w.gw) // NULL until the window is realized
		if gw == 0 {
			return window.StateNormal
		}
		return stateFromGdk(gdkWindowGetState(gw))
	})
}

func (w *win) IsFullscreen() bool {
	return w.State() == window.StateFullscreen
}

// Fullscreen before maximized: GTK reports BOTH bits once a maximized window
// goes fullscreen, and fullscreen is the one the user sees.
func stateFromGdk(flags int32) window.State {
	switch {
	case flags&gdkStateIconified != 0:
		return window.StateMinimized
	case flags&gdkStateFullscreen != 0:
		return window.StateFullscreen
	case flags&gdkStateMaximized != 0:
		return window.StateMaximized
	}
	return window.StateNormal
}

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
