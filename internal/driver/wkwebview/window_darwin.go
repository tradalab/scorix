//go:build darwin

package wkwebview

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

type manager struct {
	rt *rt

	mu   sync.Mutex
	byID map[window.ID]*win
	seq  int
}

// New must run on the UI thread (Manager contract — app.OpenWindow dispatches
// for you).
func (m *manager) New(opts window.Options) (window.Window, error) {
	style := nsWindowStyleTitled | nsWindowStyleClosable | nsWindowStyleMiniaturizable
	if opts.Resizable {
		style |= nsWindowStyleResizable
	}
	if opts.Frameless {
		style = 0 // borderless
	}
	if opts.Width == 0 {
		opts.Width = 800
	}
	if opts.Height == 0 {
		opts.Height = 600
	}

	rect := nsRect{Origin: nsPoint{X: float64(orZero(opts.X)), Y: float64(orZero(opts.Y))},
		Size: nsSize{W: float64(opts.Width), H: float64(opts.Height)}}
	nw := msgSendRectStyle(
		objc.ID(cls("NSWindow")).Send(sel("alloc")),
		sel("initWithContentRect:styleMask:backing:defer:"),
		rect, style, nsBackingStoreBuffered, false)
	if nw == 0 {
		return nil, fmt.Errorf("wkwebview: NSWindow init failed")
	}
	// We own lifetime via the manager map; don't let AppKit free it on close.
	nw.Send(sel("setReleasedWhenClosed:"), false)
	if opts.Title != "" {
		nw.Send(sel("setTitle:"), nsString(opts.Title))
	}
	if opts.Center {
		nw.Send(sel("center"))
	}

	v, err := newView(m.rt, opts)
	if err != nil {
		return nil, err
	}
	nw.Send(sel("setContentView:"), v.wk)

	m.mu.Lock()
	id := opts.ID
	if id == "" {
		m.seq++
		id = window.ID("win-" + strconv.Itoa(m.seq))
	}
	w := &win{
		id:          id,
		nw:          nw,
		rt:          m.rt,
		view:        v,
		hideOnClose: opts.HideOnClose,
		events:      map[window.Event][]func(window.EventData){},
	}
	m.byID[id] = w
	m.mu.Unlock()

	registerWinDelegate(w)

	if opts.URL != "" {
		v.Navigate(opts.URL)
	}
	return w, nil
}

func orZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
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
	nw          objc.ID // NSWindow
	rt          *rt
	view        *view
	hideOnClose bool

	mu         sync.Mutex
	events     map[window.Event][]func(window.EventData)
	closeFired bool // EventClose already fired (windowShouldClose:); windowWillClose: won't re-fire
}

// fireClose dispatches a cancelable EventClose and reports whether a handler
// vetoed via PreventDefault. Runs on the UI thread, so `prevented` needs no sync.
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

func (w *win) SetTitle(t string) { dispatchMain(func() { w.nw.Send(sel("setTitle:"), nsString(t)) }) }

func (w *win) SetSize(width, height int) {
	dispatchMain(func() {
		frame := nsRect{Size: nsSize{W: float64(width), H: float64(height)}}
		msgSendSetFrame(w.nw, sel("setFrame:display:"), frame, true)
	})
}

// Size/Position read back NSWindow.frame, a struct return through msgSend
// which needs the stret variant on amd64. Deferred to hardware validation;
// returns zeros until then (TODO: bind objc_msgSend_stret for frame).
func (w *win) Size() (int, int)     { return 0, 0 }
func (w *win) Position() (int, int) { return 0, 0 }

func (w *win) SetPosition(x, y int) {
	dispatchMain(func() {
		w.nw.Send(sel("setFrameOrigin:"), nsPoint{X: float64(x), Y: float64(y)})
	})
}

func (w *win) SetMinSize(width, height int) {
	dispatchMain(func() { w.nw.Send(sel("setMinSize:"), nsSize{W: float64(width), H: float64(height)}) })
}

func (w *win) SetMaxSize(width, height int) {
	dispatchMain(func() { w.nw.Send(sel("setMaxSize:"), nsSize{W: float64(width), H: float64(height)}) })
}

func (w *win) Center() { dispatchMain(func() { w.nw.Send(sel("center")) }) }

func (w *win) Show() {
	dispatchMain(func() {
		w.nw.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
		w.rt.app.Send(sel("activateIgnoringOtherApps:"), true)
	})
}

func (w *win) Hide() { dispatchMain(func() { w.nw.Send(sel("orderOut:"), objc.ID(0)) }) }

func (w *win) Focus() { dispatchMain(func() { w.nw.Send(sel("makeKeyAndOrderFront:"), objc.ID(0)) }) }

func (w *win) Minimize() { dispatchMain(func() { w.nw.Send(sel("miniaturize:"), objc.ID(0)) }) }

func (w *win) Maximize() { dispatchMain(func() { w.nw.Send(sel("zoom:"), objc.ID(0)) }) }

func (w *win) Unmaximize() { w.Maximize() } // zoom: toggles

func (w *win) Restore() { dispatchMain(func() { w.nw.Send(sel("deminiaturize:"), objc.ID(0)) }) }

func (w *win) SetFullscreen(bool) {
	dispatchMain(func() { w.nw.Send(sel("toggleFullScreen:"), objc.ID(0)) })
}

func (w *win) SetAlwaysOnTop(on bool) {
	level := int64(0) // NSNormalWindowLevel
	if on {
		level = 3 // NSFloatingWindowLevel
	}
	dispatchMain(func() { w.nw.Send(sel("setLevel:"), level) })
}

func (w *win) IsVisible() bool {
	ch := make(chan bool, 1)
	dispatchMain(func() { ch <- w.nw.Send(sel("isVisible")) != 0 })
	return <-ch
}

// State is not tracked (would need NSWindow notification observers); the
// minimize/maximize setters above still work.
func (w *win) State() window.State { return window.StateNormal }

func (w *win) Close() { dispatchMain(func() { w.nw.Send(sel("close")) }) }

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

var (
	winDelegateOnce sync.Once
	winByDelegate   sync.Map // delegate objc.ID -> *win
)

func registerWinDelegate(w *win) {
	winDelegateOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixWindowDelegate",
			cls("NSObject"),
			[]*objc.Protocol{objc.GetProtocol("NSWindowDelegate")},
			nil,
			[]objc.MethodDef{
				{
					// hide-on-close: intercept before the window is destroyed
					Cmd: sel("windowShouldClose:"),
					Fn: func(self objc.ID, _ objc.SEL, sender objc.ID) bool {
						defer recoverCB("windowShouldClose:")
						v, ok := winByDelegate.Load(self)
						if !ok {
							return true
						}
						w := v.(*win)
						w.mu.Lock()
						hide := w.hideOnClose
						w.mu.Unlock()
						if hide {
							w.Hide()
							return false
						}
						if w.fireClose() { // EventClose handler called PreventDefault
							return false
						}
						return true
					},
				},
				{
					Cmd: sel("windowWillClose:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
						defer recoverCB("windowWillClose:")
						v, ok := winByDelegate.Load(self)
						if !ok {
							return
						}
						w := v.(*win)
						winByDelegate.Delete(self)
						viewByWK.Delete(w.view.wk)    // drop the Go-side view pin
						w.nw.Send(sel("autorelease")) // we owned it (setReleasedWhenClosed:false)
						// PreventDefault was already offered by windowShouldClose: (which
						// sets closeFired when the close proceeds). Programmatic -close
						// skips windowShouldClose:, so fire EventClose here if it didn't.
						w.mu.Lock()
						fired := w.closeFired
						w.mu.Unlock()
						if !fired {
							w.fire(window.EventClose)
						}
						if w.rt.manager.remove(w.id) == 0 {
							w.rt.Quit()
						}
					},
				},
				{
					Cmd: sel("windowDidResize:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
						defer recoverCB("windowDidResize:")
						if v, ok := winByDelegate.Load(self); ok {
							v.(*win).fire(window.EventResize)
						}
					},
				},
				{
					Cmd: sel("windowDidBecomeKey:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
						defer recoverCB("windowDidBecomeKey:")
						if v, ok := winByDelegate.Load(self); ok {
							v.(*win).fire(window.EventFocus)
						}
					},
				},
				{
					Cmd: sel("windowDidResignKey:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
						defer recoverCB("windowDidResignKey:")
						if v, ok := winByDelegate.Load(self); ok {
							v.(*win).fire(window.EventBlur)
						}
					},
				},
			},
		)
		if err != nil {
			panic(fmt.Sprintf("wkwebview: register window delegate: %v", err))
		}
	})
	d := objc.ID(cls("ScorixWindowDelegate")).Send(sel("new"))
	winByDelegate.Store(d, w)
	w.nw.Send(sel("setDelegate:"), d)
}
