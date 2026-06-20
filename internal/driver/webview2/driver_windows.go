//go:build windows

package webview2

import (
	"fmt"
	goruntime "runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

var (
	_ window.Driver  = driver{}
	_ window.Runtime = (*runtime)(nil)
	_ window.Manager = (*manager)(nil)
	_ window.Window  = (*win)(nil)
)

func New() window.Driver { return driver{} }

type driver struct{}

func (driver) Name() string { return "webview2" }

func (driver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	r := &runtime{
		cfg:     cfg,
		events:  map[window.RuntimeEvent][]func(){},
		schemes: map[string]webview.SchemeHandler{},
	}
	r.manager = &manager{rt: r, byID: map[window.ID]*win{}, byHWND: map[windows.Handle]*win{}}
	return r, nil
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSetWindowTextW   = user32.NewProc("SetWindowTextW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")
	procSetForegroundWin = user32.NewProc("SetForegroundWindow")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsOverlappedWindow uintptr = 0x00CF0000
	wsPopup            uintptr = 0x80000000
	wsThickFrame       uintptr = 0x00040000 // resize border
	wsMaximizeBox      uintptr = 0x00010000
	wsExTopmost        uintptr = 0x00000008
	cwUseDefault       uintptr = 0x80000000 // CW_USEDEFAULT

	swHide       uintptr = 0
	swShowNormal uintptr = 1
	swMaximize   uintptr = 3
	swMinimize   uintptr = 6
	swRestore    uintptr = 9

	smCxScreen uintptr = 0
	smCyScreen uintptr = 1

	swpNoSize     uintptr = 0x0001
	swpNoMove     uintptr = 0x0002
	swpNoZOrder   uintptr = 0x0004
	swpNoActivate uintptr = 0x0010

	wmDestroy uint32 = 0x0002
	wmMove    uint32 = 0x0003
	wmSize    uint32 = 0x0005
	wmClose   uint32 = 0x0010
	wmApp     uint32 = 0x8000

	sizeMinimized uintptr = 1
	sizeMaximized uintptr = 2

	hwndMessage   = ^uintptr(2) // HWND_MESSAGE == (HWND)-3
	hwndTopmost   = ^uintptr(0) // HWND_TOPMOST == (HWND)-1
	hwndNoTopmost = ^uintptr(1) // HWND_NOTOPMOST == (HWND)-2

	colorWindow uintptr = 5     // COLOR_WINDOW; brush handle = COLOR_WINDOW+1
	idcArrow    uintptr = 32512 // IDC_ARROW
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type tagPOINT struct{ x, y int32 }

type tagMSG struct {
	hwnd     windows.Handle
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       tagPOINT
	lPrivate uint32
}

type tagRECT struct{ left, top, right, bottom int32 }

var (
	classOnce   sync.Once
	className   = mustUTF16("ScorixWindowClass")
	wndProcCB   = windows.NewCallback(wndProc)
	activeMu    sync.Mutex
	activeRT    *runtime
	classRegErr error
)

func mustUTF16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func ensureClass() error {
	classOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleW.Call(0)
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		wc := wndClassExW{
			lpfnWndProc:   wndProcCB,
			hInstance:     windows.Handle(hInst),
			hCursor:       windows.Handle(cursor),
			hbrBackground: windows.Handle(colorWindow + 1), // auto-erase to window color (no black)
			lpszClassName: className,
		}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			classRegErr = fmt.Errorf("RegisterClassEx: %w", err)
		}
	})
	return classRegErr
}

func createWindow(opts window.Options, messageOnly bool) (windows.Handle, error) {
	if err := ensureClass(); err != nil {
		return 0, err
	}
	hInst, _, _ := procGetModuleHandleW.Call(0)
	titlePtr := mustUTF16(opts.Title)

	var style, exStyle, parent uintptr
	x, y := cwUseDefault, cwUseDefault
	if messageOnly {
		parent = hwndMessage
	} else {
		style = windowStyle(opts)
		if opts.AlwaysOnTop {
			exStyle |= wsExTopmost
		}
		// Initial position; SetSize/Center still run afterward in manager.New.
		if opts.X != nil {
			x = uintptr(uint32(int32(*opts.X)))
		}
		if opts.Y != nil {
			y = uintptr(uint32(int32(*opts.Y)))
		}
	}

	hwnd, _, err := procCreateWindowExW.Call(
		exStyle,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(titlePtr)),
		style,
		x, y, 0, 0,
		parent, 0, hInst, 0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowEx: %w", err)
	}
	return windows.Handle(hwnd), nil
}

// windowStyle maps Options to a Win32 window style. Frameless drops the
// caption/border (WS_POPUP); non-resizable drops the resize border + maximize box.
func windowStyle(opts window.Options) uintptr {
	if opts.Frameless {
		style := wsPopup
		if opts.Resizable {
			style |= wsThickFrame
		}
		return style
	}
	style := wsOverlappedWindow
	if !opts.Resizable {
		style &^= wsThickFrame | wsMaximizeBox
	}
	return style
}

// wndProc must take only uintptr-sized args (windows.NewCallback requirement).
func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	// Invoked by DispatchMessage (C), where a Go panic is undefined behavior;
	// contain it so a handler bug can't crash the app.
	defer func() { _ = recover() }()
	activeMu.Lock()
	rt := activeRT
	activeMu.Unlock()

	if rt != nil {
		h := windows.Handle(hwnd)
		switch uint32(message) {
		case wmApp:
			rt.drainTasks()
			return 0
		case wmSize:
			if w := rt.manager.byHandle(h); w != nil {
				w.handleSize(wParam)
			}
			return 0
		case wmMove:
			if w := rt.manager.byHandle(h); w != nil {
				w.fire(window.EventMove)
			}
			return 0
		case wmClose:
			if w := rt.manager.byHandle(h); w != nil {
				if w.hideOnCloseEnabled() {
					procShowWindow.Call(hwnd, swHide)
					return 0
				}
				if w.fireClose() { // an EventClose handler called PreventDefault
					return 0 // returning 0 from WM_CLOSE cancels the close
				}
			}
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			if w := rt.manager.byHandle(h); w != nil {
				w.dispose()
			}
			if rt.manager.remove(h) == 0 {
				procPostQuitMessage.Call(0)
			}
			return 0
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return ret
}

type runtime struct {
	cfg     window.RuntimeConfig
	manager *manager

	mu      sync.Mutex
	tasks   []func()
	events  map[window.RuntimeEvent][]func()
	schemes map[string]webview.SchemeHandler
	msgHWND windows.Handle
}

func (r *runtime) Run() error {
	goruntime.LockOSThread()
	coInitSTA() // WebView2 requires an STA UI thread

	activeMu.Lock()
	activeRT = r
	activeMu.Unlock()

	hwnd, err := createWindow(window.Options{}, true) // message-only window for Dispatch wakeups
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.msgHWND = hwnd
	r.mu.Unlock()

	r.fire(window.RuntimeReady)

	var m tagMSG
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			return fmt.Errorf("GetMessage failed")
		case 0:
			return nil // WM_QUIT
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

func (r *runtime) Quit() {
	r.fire(window.RuntimeBeforeQuit)
	procPostQuitMessage.Call(0)
}

func (r *runtime) Dispatch(fn func()) {
	r.mu.Lock()
	r.tasks = append(r.tasks, fn)
	hwnd := r.msgHWND
	r.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), uintptr(wmApp), 0, 0)
	}
}

func (r *runtime) drainTasks() {
	r.mu.Lock()
	tasks := r.tasks
	r.tasks = nil
	r.mu.Unlock()
	for _, fn := range tasks {
		fn()
	}
}

func (r *runtime) Windows() window.Manager { return r.manager }

func (r *runtime) RegisterScheme(scheme string, h webview.SchemeHandler) {
	r.mu.Lock()
	r.schemes[scheme] = h
	r.mu.Unlock()
}

func (r *runtime) On(evt window.RuntimeEvent, fn func()) {
	r.mu.Lock()
	r.events[evt] = append(r.events[evt], fn)
	r.mu.Unlock()
}

func (r *runtime) fire(evt window.RuntimeEvent) {
	r.mu.Lock()
	fns := append([]func(){}, r.events[evt]...)
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

type manager struct {
	rt     *runtime
	mu     sync.Mutex
	byID   map[window.ID]*win
	byHWND map[windows.Handle]*win
	seq    int
}

func (m *manager) New(opts window.Options) (window.Window, error) {
	hwnd, err := createWindow(opts, false)
	if err != nil {
		return nil, err
	}
	w := &win{
		hwnd:        hwnd,
		opts:        opts,
		hideOnClose: opts.HideOnClose,
		rt:          m.rt,
		view:        newView(m.rt.Dispatch),
		events:      map[window.Event][]func(window.EventData){},
	}
	if opts.InitScript != "" {
		w.view.InitScript(opts.InitScript)
	}
	if opts.URL != "" {
		w.view.startURL = opts.URL
	}

	m.mu.Lock()
	m.seq++
	id := opts.ID
	if id == "" {
		id = window.ID(fmt.Sprintf("win-%d", m.seq))
	}
	w.id = id
	m.byID[id] = w
	m.byHWND[hwnd] = w
	m.mu.Unlock()

	if opts.Width > 0 && opts.Height > 0 {
		w.SetSize(opts.Width, opts.Height)
	}
	if opts.Center {
		w.Center()
	}

	// Bring up the WebView2 control asynchronously; the callbacks run on the UI
	// thread (this thread, once the message loop is pumping). A failure here
	// leaves a usable blank native window.
	if err := w.startAttach(m.rt.cfg.Identifier); err != nil {
		return w, fmt.Errorf("webview2 attach: %w", err)
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

func (m *manager) byHandle(hwnd windows.Handle) *win {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byHWND[hwnd]
}

func (m *manager) remove(hwnd windows.Handle) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.byHWND[hwnd]; ok {
		delete(m.byHWND, hwnd)
		delete(m.byID, w.id)
	}
	return len(m.byHWND)
}

type win struct {
	mu          sync.Mutex
	id          window.ID
	hwnd        windows.Handle
	opts        window.Options
	state       window.State
	hideOnClose bool
	rt          *runtime
	view        *webview2View
	controller  unsafe.Pointer // ICoreWebView2Controller (set async)
	core        unsafe.Pointer // ICoreWebView2 (set async)
	env         unsafe.Pointer // ICoreWebView2Environment (set async)
	events      map[window.Event][]func(window.EventData)
	handlers    handlerSet // per-window COM callbacks, unpinned on dispose
	envKeep     []any      // env-options objects pinned in handlerKeep, unpinned on dispose
	closeFired  bool       // EventClose already fired (WM_CLOSE path); dispose won't re-fire
}

func (w *win) ID() window.ID      { return w.id }
func (w *win) View() webview.View { return w.view }

// startAttach kicks off the async WebView2 bring-up. onCore runs on the UI
// thread and wires bounds, the JS->Go message channel and the initial nav.
func (w *win) startAttach(_ string) error {
	// Collect registered scheme names to register as custom schemes at env
	// creation (must happen before navigation to scorix://...).
	var schemeNames []string
	if w.rt != nil {
		w.rt.mu.Lock()
		for k := range w.rt.schemes {
			schemeNames = append(schemeNames, k)
		}
		w.rt.mu.Unlock()
	}
	// Empty userDataFolder => WebView2 picks the default per-user location.
	// w.handlers.add tracks the env/controller completion handlers so they're
	// unpinned when the window is disposed.
	keepEnv := func(objs []any) { w.mu.Lock(); w.envKeep = objs; w.mu.Unlock() }
	return createEnvironment(w.hwnd, "", schemeNames, w.handlers.add, keepEnv, func(core, controller, env unsafe.Pointer) {
		// controller/env are borrowed here; AddRef to retain them or WebView2
		// releases them and our stored pointers dangle (crash on next use, e.g.
		// WM_SIZE -> PutBounds). `core` is NOT AddRef'd: it came from
		// get_CoreWebView2 (comCallOut), an [out,retval] getter that already returns
		// an owned ref — dispose()'s single Release balances it. (AddRef'ing here too
		// was a +1 leak per window. NEEDS Windows runtime validation.)
		comCall(controller, iunknownAddRef)
		comCall(env, iunknownAddRef)

		w.mu.Lock()
		w.core, w.controller, w.env = core, controller, env
		w.mu.Unlock()

		comCall(controller, ctrlPutIsVisible, 1)
		w.updateBounds()

		// Wire any registered custom-scheme handlers (in-process asset serving).
		if w.rt != nil {
			w.rt.mu.Lock()
			schemes := make(map[string]webview.SchemeHandler, len(w.rt.schemes))
			for k, v := range w.rt.schemes {
				schemes[k] = v
			}
			w.rt.mu.Unlock()
			for scheme, h := range schemes {
				w.handlers.add(wireScheme(core, env, scheme, h))
			}
		}

		msgHandler := w.handlers.add(newHandler(func(_ /*sender*/, args unsafe.Pointer) {
			p, _ := comCallOut(args, argsTryGetWebMessageAsString)
			if p != nil {
				s := windows.UTF16PtrToString((*uint16)(p))
				coTaskMemFree(p)
				w.view.deliver([]byte(s))
			}
		}))
		token := new(int64) // heap: GC-stable address for the out-param
		comCall(core, cwvAddWebMessageReceived, uintptr(unsafe.Pointer(msgHandler)), uintptr(unsafe.Pointer(token)))

		w.view.ready(core)
	})
}

func (w *win) updateBounds() {
	w.mu.Lock()
	controller := w.controller
	w.mu.Unlock()
	if controller == nil {
		return
	}
	var r tagRECT
	procGetClientRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&r)))
	comCall(controller, ctrlPutBounds, uintptr(unsafe.Pointer(&r)))
}

func (w *win) SetTitle(title string) {
	procSetWindowTextW.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(mustUTF16(title))))
}

func (w *win) SetSize(cw, ch int) {
	cw, ch = clampSize(cw, ch, w.opts.MinWidth, w.opts.MinHeight, w.opts.MaxWidth, w.opts.MaxHeight)
	procSetWindowPos.Call(uintptr(w.hwnd), 0, 0, 0, uintptr(cw), uintptr(ch), swpNoMove|swpNoZOrder|swpNoActivate)
}

func (w *win) Size() (int, int) {
	var r tagRECT
	procGetWindowRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&r)))
	return int(r.right - r.left), int(r.bottom - r.top)
}

func (w *win) SetPosition(x, y int) {
	procSetWindowPos.Call(uintptr(w.hwnd), 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

func (w *win) Position() (int, int) {
	var r tagRECT
	procGetWindowRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&r)))
	return int(r.left), int(r.top)
}

func (w *win) SetMinSize(a, b int) {
	w.mu.Lock()
	w.opts.MinWidth, w.opts.MinHeight = a, b
	w.mu.Unlock()
}

func (w *win) SetMaxSize(a, b int) {
	w.mu.Lock()
	w.opts.MaxWidth, w.opts.MaxHeight = a, b
	w.mu.Unlock()
}

func (w *win) Center() {
	cw, ch := w.Size()
	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	x, y := centerPosition(cw, ch, int(sw), int(sh))
	w.SetPosition(x, y)
}

func (w *win) Show() {
	procShowWindow.Call(uintptr(w.hwnd), swShowNormal)
	procSetForegroundWin.Call(uintptr(w.hwnd))
}
func (w *win) Hide()       { procShowWindow.Call(uintptr(w.hwnd), swHide) }
func (w *win) Focus()      { procSetForegroundWin.Call(uintptr(w.hwnd)) }
func (w *win) Minimize()   { procShowWindow.Call(uintptr(w.hwnd), swMinimize) }
func (w *win) Maximize()   { procShowWindow.Call(uintptr(w.hwnd), swMaximize) }
func (w *win) Unmaximize() { procShowWindow.Call(uintptr(w.hwnd), swRestore) }
func (w *win) Restore()    { procShowWindow.Call(uintptr(w.hwnd), swRestore) }

func (w *win) SetFullscreen(on bool) {
	// Approximation via maximize; true borderless-fullscreen (style swap +
	// monitor rect) is a follow-up.
	if on {
		procShowWindow.Call(uintptr(w.hwnd), swMaximize)
		return
	}
	procShowWindow.Call(uintptr(w.hwnd), swRestore)
}

func (w *win) SetAlwaysOnTop(on bool) {
	after := hwndNoTopmost
	if on {
		after = hwndTopmost
	}
	procSetWindowPos.Call(uintptr(w.hwnd), after, 0, 0, 0, 0, swpNoMove|swpNoSize)
}

func (w *win) IsVisible() bool {
	r, _, _ := procIsWindowVisible.Call(uintptr(w.hwnd))
	return r != 0
}

func (w *win) State() window.State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// Close destroys the window via the runtime dispatch, since DestroyWindow must
// run on the creating (UI) thread.
//
// Close destroys the window. Programmatic close goes straight to WM_DESTROY
// (bypassing WM_CLOSE/hideOnClose/PreventDefault — code that calls Close means
// it); dispose() fires EventClose on that path so app teardown still runs.
func (w *win) Close() {
	hwnd := w.hwnd
	if w.rt != nil {
		w.rt.Dispatch(func() { procDestroyWindow.Call(uintptr(hwnd)) })
		return
	}
	procDestroyWindow.Call(uintptr(hwnd))
}

// dispose closes the controller and releases the COM objects AddRef'd in
// startAttach (controller/core/env). Called on WM_DESTROY. Idempotent.
func (w *win) dispose() {
	w.mu.Lock()
	fireClose := !w.closeFired // WM_CLOSE already fired it on a user close
	w.closeFired = true
	core, controller, env := w.core, w.controller, w.env
	w.core, w.controller, w.env = nil, nil, nil
	envKeep := w.envKeep
	w.envKeep = nil
	w.mu.Unlock()
	// On a programmatic Close (no WM_CLOSE), fire EventClose here so the app's
	// per-window teardown (removeSender, bridge removal, winClose drain) runs.
	if fireClose {
		w.fire(window.EventClose)
	}
	if controller != nil {
		comCall(controller, ctrlClose)
		comCall(controller, iunknownRelease)
	}
	if core != nil {
		comCall(core, iunknownRelease)
	}
	if env != nil {
		comCall(env, iunknownRelease)
	}
	// WebView2 has released its refs; now unpin our per-window callbacks AND the
	// env-options graph so they (and the *win they capture) can be collected.
	w.handlers.release()
	for _, o := range envKeep {
		handlerKeep.Delete(o)
	}
}
func (w *win) SetHideOnClose(on bool) { w.mu.Lock(); w.hideOnClose = on; w.mu.Unlock() }

func (w *win) hideOnCloseEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hideOnClose
}

func (w *win) On(evt window.Event, fn func(window.EventData)) {
	w.mu.Lock()
	w.events[evt] = append(w.events[evt], fn)
	w.mu.Unlock()
}

func (w *win) handleSize(kind uintptr) {
	w.mu.Lock()
	switch kind {
	case sizeMinimized:
		w.state = window.StateMinimized
	case sizeMaximized:
		w.state = window.StateMaximized
	default:
		w.state = window.StateNormal
	}
	w.mu.Unlock()

	w.updateBounds() // keep the WebView2 control filling the client area

	switch kind {
	case sizeMinimized:
		w.fire(window.EventMinimize)
	case sizeMaximized:
		w.fire(window.EventMaximize)
	default:
		w.fire(window.EventResize)
	}
}

func (w *win) fire(evt window.Event) {
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[evt]...)
	id := w.id
	w.mu.Unlock()
	for _, fn := range fns {
		fn(window.EventData{Window: id})
	}
}

// fireClose dispatches EventClose and reports whether a handler vetoed via
// PreventDefault. Handlers run synchronously on the UI thread, so `prevented`
// needs no synchronization.
func (w *win) fireClose() (prevented bool) {
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[window.EventClose]...)
	id := w.id
	w.mu.Unlock()
	data := window.EventData{Window: id, PreventDefault: func() { prevented = true }}
	for _, fn := range fns {
		fn(data)
	}
	if !prevented { // close proceeds to WM_DESTROY; dispose must not re-fire EventClose
		w.mu.Lock()
		w.closeFired = true
		w.mu.Unlock()
	}
	return prevented
}
