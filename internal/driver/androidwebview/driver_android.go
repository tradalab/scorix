//go:build android && cgo

package androidwebview

/*
#cgo LDFLAGS: -llog
#include <stdlib.h>
#include "host.h"
*/
import "C"

import (
	"bufio"
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
	"syscall"
	"unsafe"

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
	_ webview.View       = (*view)(nil)
)

// Host operations; dev.scorix.ScorixHost.fromGo switches on the same numbers.
const (
	opPost       = 1
	opWake       = 2
	opOpen       = 3
	opEval       = 4
	opNavigate   = 5
	opLoadHTML   = 6
	opSetTitle   = 7
	opFinish     = 8
	opInitScript = 9
	opCall       = 10
	opCallCancel = 11
	opSetTheme   = 12
)

// Lifecycle codes from ScorixActivity.
const (
	lifeForeground = 1
	lifeBackground = 2
	lifeLowMemory  = 3
)

func New() window.Driver { return driver{} }

type driver struct{}

func (driver) Name() string { return "androidwebview" }

func (driver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	r := &runtime{
		quit:    make(chan struct{}),
		events:  map[window.RuntimeEvent][]func(){},
		schemes: map[string]webview.SchemeHandler{},
	}
	r.manager = &manager{rt: r}
	return r, nil
}

// JNI entry points are free functions, so they find the app through this.
var active atomic.Pointer[runtime]

type runtime struct {
	manager  *manager
	quit     chan struct{}
	quitOnce sync.Once

	mu      sync.Mutex
	closed  bool
	tasks   []func()
	events  map[window.RuntimeEvent][]func()
	schemes map[string]webview.SchemeHandler
}

// The host's Looper is the event loop; Run only parks main until Quit.
func (r *runtime) Run() error {
	if !active.CompareAndSwap(nil, r) {
		return errors.New("androidwebview: a process runs one runtime")
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
		toHost(opFinish, nil)
	})
}

// Wake only on an empty queue: one Looper message drains everything queued before it.
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
		toHost(opWake, nil)
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

// At most one window: an Activity shows a single WebView.
type manager struct {
	rt *runtime
	mu sync.Mutex
	w  *win
}

func (m *manager) New(opts window.Options) (window.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w != nil {
		return nil, errors.New("androidwebview: Android shows one window")
	}
	id := opts.ID
	if id == "" {
		id = "main"
	}
	w := &win{
		rt:     m.rt,
		id:     id,
		w:      opts.Width,
		h:      opts.Height,
		view:   &view{},
		events: map[window.Event][]func(window.EventData){},
	}
	m.w = w
	url := toWebURL(opts.URL)
	spec, _ := json.Marshal(map[string]any{
		"url":        url,
		"origin":     originOf(url),
		"title":      opts.Title,
		"initScript": opts.InitScript,
		"devTools":   opts.DevTools,
		"secure":     opts.Secure,
		"theme":      opts.Theme,
	})
	toHost(opOpen, spec)
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

// Outlives Activity recreation, so geometry and window-manager calls only record or ignore.
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
func (w *win) SetTitle(t string)    { toHost(opSetTitle, []byte(t)) }
func (w *win) SetTheme(t string)    { toHost(opSetTheme, []byte(t)) }
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

// The only window closing ends the app and finishes the Activity.
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
}

func (v *view) Navigate(url string)  { toHost(opNavigate, []byte(toWebURL(url))) }
func (v *view) LoadHTML(html string) { toHost(opLoadHTML, []byte(html)) }
func (v *view) InitScript(js string) { toHost(opInitScript, []byte(js)) }
func (v *view) Eval(js string)       { toHost(opEval, []byte(js)) }
func (v *view) OpenDevTools()        {}

func (v *view) OnMessage(fn func(raw []byte)) {
	v.mu.Lock()
	v.onMsg = fn
	v.mu.Unlock()
}

// No error possible: the host silently drops frames sent before the page says hello.
func (v *view) PostMessage(raw []byte) error {
	toHost(opPost, raw)
	return nil
}

func toHost(op int, data []byte) {
	var p unsafe.Pointer
	if len(data) > 0 {
		p = unsafe.Pointer(&data[0])
	}
	C.scorix_to_host(C.int(op), p, C.int(len(data)))
}

//go:linkname mainMain main.main
func mainMain()

var startOnce sync.Once

// The process exits when main returns so the next launch does not find a Go
// runtime with no app inside.
//
//export scorixStart
func scorixStart(env *C.char, n C.int) {
	vars := C.GoBytes(unsafe.Pointer(env), n)
	startOnce.Do(func() {
		for _, kv := range strings.Split(string(vars), "\n") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				_ = os.Setenv(k, v)
			}
		}
		nativecall.Install(
			func(call []byte) { toHost(opCall, call) },
			func(id []byte) { toHost(opCallCancel, id) },
		)
		go func() {
			mainMain()
			os.Exit(0)
		}()
	})
}

//export scorixMessage
func scorixMessage(data *C.char, n C.int) {
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
		fn(C.GoBytes(unsafe.Pointer(data), n))
	}
}

//export scorixCallReply
func scorixCallReply(data *C.char, n C.int) {
	nativecall.Deliver(C.GoBytes(unsafe.Pointer(data), n))
}

//export scorixRunTasks
func scorixRunTasks() {
	if r := active.Load(); r != nil {
		r.runTasks()
	}
}

// The launch intent is read in onCreate before main sets OnOpenURL, so links queue.
// Delivery is off the UI thread: a handler making a native round trip would deadlock it.
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

// Called with openMu held.
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

//export scorixOpenURL
func scorixOpenURL(data *C.char, n C.int) {
	u := string(C.GoBytes(unsafe.Pointer(data), n))
	openMu.Lock()
	openPending = append(openPending, u)
	drainOpen()
	openMu.Unlock()
}

//export scorixLifecycle
func scorixLifecycle(code C.int) {
	r := active.Load()
	if r == nil {
		return
	}
	switch code {
	case lifeForeground:
		r.fire(window.RuntimeForeground)
	case lifeBackground:
		r.fire(window.RuntimeBackground)
	case lifeLowMemory:
		r.fire(window.RuntimeLowMemory)
	}
}

// Runs on a WebView worker thread, so blocking is fine. Android never exposes
// the request body, so a POST reaches the handler without one.
//
//export scorixRequest
func scorixRequest(data *C.char, n C.int, outLen *C.int) unsafe.Pointer {
	out := serveRequest(C.GoBytes(unsafe.Pointer(data), n))
	*outLen = C.int(len(out))
	p := C.malloc(C.size_t(len(out)))
	copy(unsafe.Slice((*byte)(p), len(out)), out)
	return p
}

func serveRequest(packed []byte) []byte {
	lines := strings.Split(string(packed), "\n")
	method, raw, _ := strings.Cut(lines[0], " ")
	header := http.Header{}
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ": "); ok {
			header.Add(k, v)
		}
	}
	appURL, ok := fromWebURL(raw)
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

// Format must match ScorixHost.toResponse. The reason phrase is required because
// WebResourceResponse throws on an empty one.
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

// Android sends stdout/stderr to /dev/null; redirect to logcat in init so
// app package inits and panic traces are visible.
func init() {
	pr, pw, err := os.Pipe()
	if err != nil {
		return
	}
	_ = syscall.Dup3(int(pw.Fd()), 1, 0)
	_ = syscall.Dup3(int(pw.Fd()), 2, 0)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			line := C.CString(sc.Text())
			C.scorix_log(line)
			C.free(unsafe.Pointer(line))
		}
	}()
}
