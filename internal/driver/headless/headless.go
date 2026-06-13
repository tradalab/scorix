// Package headless is a pure-Go window.Driver with no native window, for web mode and tests.
package headless

import (
	"strconv"
	"sync"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

var (
	_ window.Driver  = driver{}
	_ window.Runtime = (*runtime)(nil)
	_ window.Manager = (*manager)(nil)
	_ window.Window  = (*win)(nil)
	_ webview.View   = (*view)(nil)
)

func New() window.Driver { return driver{} }

type driver struct{}

func (driver) Name() string { return "headless" }

func (driver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	return &runtime{
		manager: &manager{windows: map[window.ID]*win{}},
		quit:    make(chan struct{}),
		tasks:   make(chan func(), 64),
		events:  map[window.RuntimeEvent][]func(){},
		schemes: map[string]webview.SchemeHandler{},
	}, nil
}

type runtime struct {
	manager *manager

	quit   chan struct{}
	tasks  chan func()
	closed bool

	mu      sync.Mutex
	events  map[window.RuntimeEvent][]func()
	schemes map[string]webview.SchemeHandler
}

func (r *runtime) Run() error {
	r.fire(window.RuntimeReady)
	for {
		select {
		case fn := <-r.tasks:
			fn()
		case <-r.quit:
			return nil
		}
	}
}

func (r *runtime) Quit() {
	r.fire(window.RuntimeBeforeQuit)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.quit)
	}
}

func (r *runtime) Dispatch(fn func()) {
	select {
	case r.tasks <- fn:
	case <-r.quit:
	}
}

func (r *runtime) Windows() window.Manager { return r.manager }

func (r *runtime) RegisterScheme(scheme string, h webview.SchemeHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schemes[scheme] = h
}

func (r *runtime) On(evt window.RuntimeEvent, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[evt] = append(r.events[evt], fn)
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
	mu      sync.Mutex
	windows map[window.ID]*win
	seq     int
}

func (m *manager) New(opts window.Options) (window.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := opts.ID
	if id == "" {
		m.seq++
		id = window.ID("win-" + strconv.Itoa(m.seq))
	}
	w := &win{
		id:      id,
		w:       opts.Width,
		h:       opts.Height,
		visible: true,
		view:    &view{},
		events:  map[window.Event][]func(window.EventData){},
	}
	m.windows[id] = w
	return w, nil
}

func (m *manager) Get(id window.ID) (window.Window, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.windows[id]
	return w, ok
}

func (m *manager) All() []window.Window {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]window.Window, 0, len(m.windows))
	for _, w := range m.windows {
		out = append(out, w)
	}
	return out
}

func (m *manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.windows)
}

type win struct {
	mu      sync.Mutex
	id      window.ID
	w, h    int
	x, y    int
	state   window.State
	visible bool
	view    *view
	events  map[window.Event][]func(window.EventData)
}

func (w *win) ID() window.ID      { return w.id }
func (w *win) View() webview.View { return w.view }

func (w *win) SetTitle(string) {}

func (w *win) SetSize(a, b int) {
	w.mu.Lock()
	w.w, w.h = a, b
	w.mu.Unlock()
	w.fire(window.EventResize)
}

func (w *win) Size() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w, w.h
}

func (w *win) SetPosition(a, b int) {
	w.mu.Lock()
	w.x, w.y = a, b
	w.mu.Unlock()
	w.fire(window.EventMove)
}

func (w *win) Position() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.x, w.y
}

func (w *win) SetMinSize(int, int) {}
func (w *win) SetMaxSize(int, int) {}
func (w *win) Center()             {}

func (w *win) Show() {
	w.mu.Lock()
	w.visible = true
	w.mu.Unlock()
	w.fire(window.EventFocus)
}

func (w *win) Hide() {
	w.mu.Lock()
	w.visible = false
	w.mu.Unlock()
	w.fire(window.EventBlur)
}

func (w *win) Focus()      { w.fire(window.EventFocus) }
func (w *win) Minimize()   { w.setState(window.StateMinimized, window.EventMinimize) }
func (w *win) Maximize()   { w.setState(window.StateMaximized, window.EventMaximize) }
func (w *win) Unmaximize() { w.setState(window.StateNormal, window.EventResize) }
func (w *win) Restore()    { w.setState(window.StateNormal, window.EventResize) }

func (w *win) SetFullscreen(on bool) {
	if on {
		w.setState(window.StateFullscreen, window.EventResize)
		return
	}
	w.setState(window.StateNormal, window.EventResize)
}

func (w *win) SetAlwaysOnTop(bool) {}

func (w *win) IsVisible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible
}

func (w *win) State() window.State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *win) Close()              { w.fire(window.EventClose) }
func (w *win) SetHideOnClose(bool) {}

func (w *win) On(evt window.Event, fn func(window.EventData)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events[evt] = append(w.events[evt], fn)
}

func (w *win) setState(s window.State, evt window.Event) {
	w.mu.Lock()
	w.state = s
	w.mu.Unlock()
	w.fire(evt)
}

func (w *win) fire(evt window.Event) {
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[evt]...)
	data := window.EventData{Window: w.id, W: w.w, H: w.h, X: w.x, Y: w.y}
	w.mu.Unlock()
	for _, fn := range fns {
		fn(data)
	}
}

type view struct {
	mu    sync.Mutex
	onMsg func(raw []byte)
	toJS  [][]byte
}

func (v *view) Navigate(string)   {}
func (v *view) LoadHTML(string)   {}
func (v *view) InitScript(string) {}
func (v *view) Eval(string)       {}
func (v *view) OpenDevTools()     {}

func (v *view) OnMessage(fn func(raw []byte)) {
	v.mu.Lock()
	v.onMsg = fn
	v.mu.Unlock()
}

func (v *view) PostMessage(raw []byte) error {
	v.mu.Lock()
	v.toJS = append(v.toJS, append([]byte(nil), raw...))
	v.mu.Unlock()
	return nil
}

// Inject simulates a JS -> Go message on a window's view.
func Inject(w window.Window, raw []byte) {
	if v, ok := w.View().(*view); ok {
		v.mu.Lock()
		fn := v.onMsg
		v.mu.Unlock()
		if fn != nil {
			fn(raw)
		}
	}
}

// Sent returns the Go -> JS messages pushed to a window's view, for assertions.
func Sent(w window.Window) [][]byte {
	if v, ok := w.View().(*view); ok {
		v.mu.Lock()
		defer v.mu.Unlock()
		return append([][]byte{}, v.toJS...)
	}
	return nil
}
