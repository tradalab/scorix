//go:build darwin

package wkwebview

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/internal/ico"
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

// New must run on the UI thread (Manager contract - app.OpenWindow dispatches
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

	rect := nsRect{Size: nsSize{W: float64(opts.Width), H: float64(opts.Height)}}
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

	// Before SetSize, which clamps to them: the window must not open outside its limits.
	if opts.MinWidth > 0 || opts.MinHeight > 0 {
		w.SetMinSize(opts.MinWidth, opts.MinHeight) // AppKit's own default min is 0
	}
	if opts.MaxWidth > 0 || opts.MaxHeight > 0 {
		w.SetMaxSize(opts.MaxWidth, opts.MaxHeight)
	}

	// Through the same methods the framework calls later, so creation cannot
	// disagree with them: initWithContentRect: took a CONTENT rect in AppKit's
	// space, while opts carries an OUTER size measured from the top-left.
	if opts.Width > 0 && opts.Height > 0 {
		w.SetSize(opts.Width, opts.Height)
	}
	switch {
	case opts.Center:
		w.Center()
	case opts.X != nil || opts.Y != nil:
		x, y := w.Position() // keep whichever axis the caller left unset
		if opts.X != nil {
			x = *opts.X
		}
		if opts.Y != nil {
			y = *opts.Y
		}
		w.SetPosition(x, y)
	}
	if opts.AlwaysOnTop {
		w.SetAlwaysOnTop(true)
	}
	if opts.IconPath != "" {
		setAppIcon(opts.IconPath)
	}

	if opts.URL != "" {
		v.Navigate(opts.URL)
	}
	return w, nil
}

// A window with one axis capped needs a value for the other: NSWindow reads 0 as
// "cannot grow at all", not as "unlimited", and its own default is FLT_MAX.
const nsSizeUnlimited = 1 << 24

func orUnlimited(v int) int {
	if v <= 0 {
		return nsSizeUnlimited
	}
	return v
}

var appIconOnce sync.Once

// macOS has no per-window icon: the Dock reads CFBundleIconFile from the bundle, so
// app.icon only stands in for a run straight from the binary.
func setAppIcon(path string) {
	appIconOnce.Do(func() {
		if objc.ID(cls("NSBundle")).Send(sel("mainBundle")).Send(sel("bundleIdentifier")) != 0 {
			return
		}
		img := loadImage(path)
		if img == 0 {
			if _, err := os.Stat(path); err == nil {
				logger.Warn("wkwebview: app.icon could not be loaded as the Dock icon", "path", path)
			}
			return
		}
		app := objc.ID(cls("NSApplication")).Send(sel("sharedApplication"))
		app.Send(sel("setApplicationIconImage:"), img)
		img.Send(sel("release"))
	})
}

// 0 for a missing or undecodable file. An ICO goes in as its largest PNG frame.
func loadImage(path string) objc.ID {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return 0
	}
	if frame, ok := ico.LargestPNG(data); ok {
		data = frame
	}
	nsData := msgSendBytesLen(objc.ID(cls("NSData")), sel("dataWithBytes:length:"), unsafe.Pointer(&data[0]), uint64(len(data)))
	img := objc.ID(cls("NSImage")).Send(sel("alloc")).Send(sel("initWithData:"), nsData)
	if img == 0 {
		return 0
	}
	if !isYes(img, "isValid") {
		img.Send(sel("release"))
		return 0
	}
	return img
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

// A frame carries the origin too, so one built from zeros sends the window to the
// corner. See pinTop for why the top edge is the one held still.
func (w *win) SetSize(width, height int) {
	onMain(func() {
		f, ok := rectOf(w.nw, "frame")
		if !ok {
			logger.Warn("wkwebview: cannot read the frame, window not resized")
			return
		}
		// AppKit documents minSize/maxSize for the user's resize, not for setFrame.
		lo, _ := sizeOf(w.nw, "minSize")
		hi, _ := sizeOf(w.nw, "maxSize")
		size := clampToLimits(nsSize{W: float64(width), H: float64(height)}, lo, hi)
		f.Origin.Y = pinTop(f.Origin.Y, f.Size.H, size.H)
		f.Size = size
		msgSendSetFrame(w.nw, sel("setFrame:display:"), f, true)
	})
}

func clampToLimits(s, lo, hi nsSize) nsSize {
	if lo.W > 0 && s.W < lo.W {
		s.W = lo.W
	}
	if lo.H > 0 && s.H < lo.H {
		s.H = lo.H
	}
	if hi.W > 0 && s.W > hi.W {
		s.W = hi.W
	}
	if hi.H > 0 && s.H > hi.H {
		s.H = hi.H
	}
	return s
}

func (w *win) Size() (int, int) {
	sz := onMainVal(func() nsSize {
		f, _ := rectOf(w.nw, "frame")
		return f.Size
	})
	return int(sz.W), int(sz.H)
}

func (w *win) Position() (int, int) {
	p := onMainVal(func() nsPoint {
		f, ok := rectOf(w.nw, "frame")
		if !ok {
			return nsPoint{}
		}
		y := f.Origin.Y
		if h, ok := primaryScreenHeight(); ok {
			y = flipY(h, f.Origin.Y, f.Size.H)
		}
		return nsPoint{X: f.Origin.X, Y: y}
	})
	return int(p.X), int(p.Y)
}

// The caller's y is measured from the TOP; passing it straight through put the
// window at the wrong end of the screen.
func (w *win) SetPosition(x, y int) {
	onMain(func() {
		f, ok := rectOf(w.nw, "frame")
		if !ok {
			logger.Warn("wkwebview: cannot read the frame, window not moved")
			return
		}
		oy := float64(y)
		if h, ok := primaryScreenHeight(); ok {
			oy = flipY(h, float64(y), f.Size.H)
		}
		w.nw.Send(sel("setFrameOrigin:"), nsPoint{X: float64(x), Y: oy})
	})
}

// flipY converts between AppKit's bottom-left origin and the top-left the rest of
// the framework uses. Its OWN INVERSE, which is what makes the pair agree.
func flipY(screenH, y, winH float64) float64 { return screenH - (y + winH) }

// pinTop leaves the TOP edge where it is while the height changes: AppKit grows a
// window upwards, so without this a resize walks the title bar down the screen.
func pinTop(originY, oldH, newH float64) float64 { return originY + oldH - newH }

// onMain: a queued setter would land after the SetSize it must bound.
func (w *win) SetMinSize(width, height int) {
	onMain(func() { w.nw.Send(sel("setMinSize:"), nsSize{W: float64(width), H: float64(height)}) })
}

func (w *win) SetMaxSize(width, height int) {
	s := nsSize{W: float64(orUnlimited(width)), H: float64(orUnlimited(height))}
	onMain(func() { w.nw.Send(sel("setMaxSize:"), s) })
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

// zoom: is a TOGGLE like toggleFullScreen:, so Maximize on an already zoomed
// window used to un-zoom it.
func (w *win) Maximize() { w.zoomTo(true) }

func (w *win) Unmaximize() { w.zoomTo(false) }

func (w *win) zoomTo(on bool) {
	onMain(func() {
		if isYes(w.nw, "isZoomed") != on {
			w.nw.Send(sel("zoom:"), objc.ID(0))
		}
	})
}

// Restore is "back to normal" everywhere else - SW_RESTORE un-minimises AND
// un-maximises - so de-miniaturising alone left a zoomed window zoomed.
func (w *win) Restore() {
	onMain(func() {
		if isYes(w.nw, "isMiniaturized") {
			w.nw.Send(sel("deminiaturize:"), objc.ID(0))
		}
		if isYes(w.nw, "isZoomed") {
			w.nw.Send(sel("zoom:"), objc.ID(0))
		}
	})
}

func (w *win) StartDrag() {
	dispatchMain(func() {
		app := objc.ID(cls("NSApplication")).Send(sel("sharedApplication"))
		if ev := app.Send(sel("currentEvent")); ev != 0 {
			w.nw.Send(sel("performWindowDragWithEvent:"), ev)
		}
	})
}

// toggleFullScreen: is a TOGGLE, so calling it for a state the window is already
// in does the opposite of what the caller asked.
func (w *win) SetFullscreen(on bool) {
	onMain(func() {
		if w.fullscreen() != on {
			w.nw.Send(sel("toggleFullScreen:"), objc.ID(0))
		}
	})
}

// NSWindowStyleMaskFullScreen: AppKit has no fullscreen property of its own.
const nsWindowStyleFullScreen uint64 = 1 << 14

func (w *win) fullscreen() bool {
	return objc.Send[uint64](w.nw, sel("styleMask"))&nsWindowStyleFullScreen != 0
}

func (w *win) IsFullscreen() bool {
	return onMainVal(func() bool { return w.fullscreen() })
}

func (w *win) SetAlwaysOnTop(on bool) {
	level := int64(0) // NSNormalWindowLevel
	if on {
		level = 3 // NSFloatingWindowLevel
	}
	dispatchMain(func() { w.nw.Send(sel("setLevel:"), level) })
}

func (w *win) IsAlwaysOnTop() bool {
	return onMainVal(func() bool { return objc.Send[int64](w.nw, sel("level")) > 0 })
}

func (w *win) IsVisible() bool {
	return onMainVal(func() bool { return isYes(w.nw, "isVisible") })
}

// Asked of AppKit rather than tracked, so it stays right when the USER
// minimises or zooms the window instead of the app doing it.
func (w *win) State() window.State {
	return onMainVal(func() window.State {
		switch {
		case isYes(w.nw, "isMiniaturized"):
			return window.StateMinimized
		case w.fullscreen():
			return window.StateFullscreen
		case isYes(w.nw, "isZoomed"):
			return window.StateMaximized
		}
		return window.StateNormal
	})
}

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
