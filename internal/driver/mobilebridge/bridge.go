// Package mobilebridge is the Go half of the mobile drivers. A native host (Kotlin on
// Android, Swift on iOS) owns the process and the UI thread; it reaches Go through the
// entry points here and Go reaches it through one send function and the op numbers below.
package mobilebridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tradalab/scorix/internal/nativecall"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

var (
	_ window.Driver      = driver{}
	_ window.Runtime     = (*runtime)(nil)
	_ window.Manager     = (*manager)(nil)
	_ window.Window      = (*win)(nil)
	_ window.ThemeSetter = (*win)(nil)
	_ window.URLOpener   = (*runtime)(nil)
	_ webview.View       = (*view)(nil)
)

// Ops Go sends to the host; ScorixHost on both platforms switches on the same numbers.
const (
	OpPost       = 1
	OpWake       = 2
	OpOpen       = 3
	OpEval       = 4
	OpNavigate   = 5
	OpLoadHTML   = 6
	OpSetTitle   = 7
	OpFinish     = 8
	OpInitScript = 9
	OpCall       = 10
	OpCallCancel = 11
	OpSetTheme   = 12
)

// Lifecycle codes the host passes to Lifecycle.
const (
	LifeForeground = 1
	LifeBackground = 2
	LifeLowMemory  = 3
)

// URLs maps an app URL (scorix://app/x) to what the webview loads and back. Android serves it
// under a fixed https origin; iOS keeps the custom scheme.
type URLs struct {
	ToWeb   func(appURL string) string
	FromWeb func(webURL string) (string, bool)
	Origin  func(webURL string) string
}

var (
	sendFn atomic.Pointer[func(op int, data []byte)]
	urls   atomic.Pointer[URLs]
	active atomic.Pointer[runtime]
)

func send(op int, data []byte) {
	if fn := sendFn.Load(); fn != nil {
		(*fn)(op, data)
	}
}

// New returns the driver for a host whose ops arrive through send and whose pages live at u.
func New(name string, u URLs, sendOp func(op int, data []byte)) window.Driver {
	urls.Store(&u)
	sendFn.Store(&sendOp)
	return driver{name: name}
}

type driver struct{ name string }

func (d driver) Name() string { return d.name }

func (d driver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	r := &runtime{
		name:     d.name,
		quit:     make(chan struct{}),
		events:   map[window.RuntimeEvent][]func(){},
		schemes:  map[string]webview.SchemeHandler{},
		external: cfg.OpenExternal,
	}
	r.manager = &manager{rt: r}
	return r, nil
}

type runtime struct {
	name     string
	manager  *manager
	quit     chan struct{}
	quitOnce sync.Once

	mu      sync.Mutex
	closed  bool
	tasks   []func()
	events  map[window.RuntimeEvent][]func()
	schemes map[string]webview.SchemeHandler

	external func(url string)
}

func (r *runtime) Run() error {
	if !active.CompareAndSwap(nil, r) {
		return errors.New(r.name + ": a process runs one runtime")
	}
	r.Dispatch(func() { r.fire(window.RuntimeReady) })
	<-r.quit
	return nil
}

func (r *runtime) Quit() {
	r.quitOnce.Do(func() {
		r.fire(window.RuntimeBeforeQuit)
		r.mu.Lock()
		r.closed = true
		r.tasks = nil
		r.mu.Unlock()
		close(r.quit)
		send(OpFinish, nil)
	})
}

func (r *runtime) Dispatch(fn func()) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	wake := len(r.tasks) == 0
	r.tasks = append(r.tasks, fn)
	r.mu.Unlock()
	if wake {
		send(OpWake, nil)
	}
}

func (r *runtime) runTasks() {
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
	rt *runtime
	mu sync.Mutex
	w  *win
}

func (m *manager) New(opts window.Options) (window.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w != nil {
		return nil, errors.New(m.rt.name + ": a phone shows one window")
	}
	id := opts.ID
	if id == "" {
		id = "main"
	}
	u := urls.Load()
	url := u.ToWeb(opts.URL)
	v := &view{home: u.Origin(url)}
	v.exits.external = m.rt.external
	v.exits.guard.Trust(url)
	w := &win{
		rt:     m.rt,
		id:     id,
		w:      opts.Width,
		h:      opts.Height,
		view:   v,
		events: map[window.Event][]func(window.EventData){},
	}
	m.w = w
	spec, _ := json.Marshal(map[string]any{
		"url":        url,
		"origin":     u.Origin(url),
		"title":      opts.Title,
		"initScript": opts.InitScript,
		"devTools":   opts.DevTools,
		"secure":     opts.Secure,
		"theme":      opts.Theme,
	})
	send(OpOpen, spec)
	return w, nil
}

func (m *manager) Get(id window.ID) (window.Window, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil || m.w.id != id {
		return nil, false
	}
	return m.w, true
}

func (m *manager) All() []window.Window {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil {
		return nil
	}
	return []window.Window{m.w}
}

func (m *manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil {
		return 0
	}
	return 1
}

func (m *manager) current() *win {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.w
}

type win struct {
	rt   *runtime
	id   window.ID
	view *view

	mu     sync.Mutex
	w, h   int
	closed bool
	events map[window.Event][]func(window.EventData)
}

func (w *win) ID() window.ID        { return w.id }
func (w *win) View() webview.View   { return w.view }
func (w *win) SetTitle(t string)    { send(OpSetTitle, []byte(t)) }
func (w *win) SetTheme(t string)    { send(OpSetTheme, []byte(t)) }
func (w *win) SetPosition(x, y int) {}
func (w *win) Position() (int, int) { return 0, 0 }
func (w *win) SetMinSize(_, _ int)  {}
func (w *win) SetMaxSize(_, _ int)  {}
func (w *win) Center()              {}
func (w *win) Show()                {}
func (w *win) Hide()                {}
func (w *win) Focus()               {}
func (w *win) Minimize()            {}
func (w *win) Maximize()            {}
func (w *win) Unmaximize()          {}
func (w *win) Restore()             {}
func (w *win) SetFullscreen(bool)   {}
func (w *win) SetAlwaysOnTop(bool)  {}
func (w *win) IsVisible() bool      { return true }
func (w *win) State() window.State  { return window.StateNormal }
func (w *win) StartDrag()           {}
func (w *win) SetHideOnClose(bool)  {}

func (w *win) SetSize(width, height int) {
	w.mu.Lock()
	w.w, w.h = width, height
	w.mu.Unlock()
}

func (w *win) Size() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w, w.h
}

func (w *win) On(evt window.Event, fn func(window.EventData)) {
	w.mu.Lock()
	w.events[evt] = append(w.events[evt], fn)
	w.mu.Unlock()
}

func (w *win) Close() {
	w.rt.Dispatch(func() {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		w.closed = true
		fns := append([]func(window.EventData){}, w.events[window.EventClose]...)
		w.mu.Unlock()
		for _, fn := range fns {
			fn(window.EventData{Window: w.id})
		}
		w.rt.manager.mu.Lock()
		w.rt.manager.w = nil
		w.rt.manager.mu.Unlock()
		w.rt.Quit()
	})
}

type view struct {
	mu    sync.Mutex
	onMsg func([]byte)

	home  string
	exits exits
}

func (v *view) Navigate(url string) {
	web := urls.Load().ToWeb(url)
	v.exits.guard.Trust(web)
	send(OpNavigate, []byte(web))
}

func (v *view) LoadHTML(html string) {
	v.exits.guard.Trust(v.home)
	send(OpLoadHTML, []byte(html))
}

func (v *view) InitScript(js string) { send(OpInitScript, []byte(js)) }
func (v *view) Eval(js string)       { send(OpEval, []byte(js)) }
func (v *view) OpenDevTools()        {}

func (v *view) OnMessage(fn func(raw []byte)) {
	v.mu.Lock()
	v.onMsg = fn
	v.mu.Unlock()
}

func (v *view) PostMessage(raw []byte) error {
	send(OpPost, raw)
	return nil
}

// Start sets the environment the host passes as NAME=value lines, wires native calls through
// the host, and runs the app's main once per process; the process exits when main returns.
func Start(env []byte, main func()) {
	startOnce.Do(func() {
		for _, kv := range strings.Split(string(env), "\n") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				_ = os.Setenv(k, v)
			}
		}
		nativecall.Install(
			func(call []byte) { send(OpCall, call) },
			func(id []byte) { send(OpCallCancel, id) },
		)
		go func() {
			main()
			exit(0)
		}()
	})
}

var (
	startOnce sync.Once
	exit      = os.Exit
)

// Message hands the page's message to the window's sink.
func Message(data []byte) {
	r := active.Load()
	if r == nil {
		return
	}
	w := r.manager.current()
	if w == nil {
		return
	}
	w.view.mu.Lock()
	fn := w.view.onMsg
	w.view.mu.Unlock()
	if fn != nil {
		fn(data)
	}
}

// CallReply is a plugin's answer to a native call.
func CallReply(data []byte) { nativecall.Deliver(data) }

// RunTasks runs what Dispatch queued; the host calls it on the UI thread after OpWake.
func RunTasks() {
	if r := active.Load(); r != nil {
		r.runTasks()
	}
}

var (
	openMu       sync.Mutex
	openHandler  func(string)
	openPending  []string
	openDraining bool
)

func (r *runtime) OnOpenURL(fn func(url string)) {
	if fn == nil {
		return
	}
	openMu.Lock()
	openHandler = fn
	drainOpen()
	openMu.Unlock()
}

func drainOpen() {
	if openHandler == nil || openDraining || len(openPending) == 0 {
		return
	}
	openDraining = true
	go func() {
		for {
			openMu.Lock()
			if len(openPending) == 0 {
				openDraining = false
				openMu.Unlock()
				return
			}
			u, fn := openPending[0], openHandler
			openPending = openPending[1:]
			openMu.Unlock()
			fn(u)
		}
	}()
}

// OpenURL queues a link the app was opened with until main has set OnOpenURL.
func OpenURL(u string) {
	openMu.Lock()
	openPending = append(openPending, u)
	drainOpen()
	openMu.Unlock()
}

// KeepsOut reports whether a navigation to target must not replace the page.
func KeepsOut(target string, clicked bool) bool {
	r := active.Load()
	if r == nil {
		return false
	}
	w := r.manager.current()
	return w != nil && w.view.exits.keepsOut(target, clicked)
}

// Lifecycle turns a host lifecycle code into the runtime event.
func Lifecycle(code int) {
	r := active.Load()
	if r == nil {
		return
	}
	switch code {
	case LifeForeground:
		r.fire(window.RuntimeForeground)
	case LifeBackground:
		r.fire(window.RuntimeBackground)
	case LifeLowMemory:
		r.fire(window.RuntimeLowMemory)
	}
}

// ServeRequest answers a request packed as "METHOD url\nName: value\n...", in the packed
// response form ScorixHost parses: "status reason\nName: value\n...\n\nbody".
func ServeRequest(packed []byte) []byte {
	lines := strings.Split(string(packed), "\n")
	method, raw, _ := strings.Cut(lines[0], " ")
	header := http.Header{}
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ": "); ok {
			header.Add(k, v)
		}
	}
	appURL, ok := urls.Load().FromWeb(raw)
	r := active.Load()
	if !ok || r == nil {
		return packResponse(http.StatusNotFound, nil, nil)
	}
	scheme, _, _ := strings.Cut(appURL, ":")
	r.mu.Lock()
	h := r.schemes[scheme]
	r.mu.Unlock()
	if h == nil {
		return packResponse(http.StatusNotFound, nil, nil)
	}
	res := h(&webview.Request{Method: method, URL: appURL, Header: header})
	if res == nil {
		return packResponse(http.StatusNotFound, nil, nil)
	}
	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
	}
	return packResponse(res.Status, res.Header, body)
}

func packResponse(status int, h http.Header, body []byte) []byte {
	if status == 0 {
		status = http.StatusOK
	}
	var b bytes.Buffer
	b.WriteString(strconv.Itoa(status) + " " + http.StatusText(status) + "\n")
	for k, vs := range h {
		for _, v := range vs {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	b.WriteByte('\n')
	b.Write(body)
	return b.Bytes()
}
