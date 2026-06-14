//go:build linux

package webkitgtk

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

var (
	_ window.Driver  = driver{}
	_ window.Runtime = (*rt)(nil)
	_ window.Manager = (*manager)(nil)
	_ window.Window  = (*win)(nil)
	_ webview.View   = (*view)(nil)
)

func New() window.Driver { return driver{} }

type driver struct{}

func (driver) Name() string { return "webkitgtk" }

func (driver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	if err := initLibs(); err != nil {
		return nil, err
	}
	r := &rt{
		manager: &manager{byID: map[window.ID]*win{}},
		events:  map[window.RuntimeEvent][]func(){},
		schemes: map[string]webview.SchemeHandler{},
	}
	r.manager.rt = r

	activeMu.Lock()
	defer activeMu.Unlock()
	if activeRT != nil {
		return nil, fmt.Errorf("webkitgtk: only one Runtime per process")
	}
	activeRT = r
	return r, nil
}

var (
	activeMu sync.Mutex
	activeRT *rt
)

type rt struct {
	manager *manager

	mu      sync.Mutex
	events  map[window.RuntimeEvent][]func()
	schemes map[string]webview.SchemeHandler
}

// Run initializes GTK and blocks in gtk_main. Must be called from the main
// goroutine. RuntimeReady fires before the loop starts — widget creation is
// legal pre-gtk_main, matching how the headless driver sequences it.
func (r *rt) Run() error {
	runtime.LockOSThread()

	if gtkInitCheck(0, 0) == 0 {
		return fmt.Errorf("webkitgtk: gtk_init failed (no display?) — use web mode on headless hosts")
	}
	initDispatch()
	registerSchemes(r)

	r.fire(window.RuntimeReady)
	gtkMain()
	return nil
}

func (r *rt) Quit() {
	r.fire(window.RuntimeBeforeQuit)
	r.Dispatch(gtkMainQuit)
}

func (r *rt) Dispatch(fn func()) { dispatchMain(fn) }

func (r *rt) Windows() window.Manager { return r.manager }

func (r *rt) RegisterScheme(scheme string, h webview.SchemeHandler) {
	r.mu.Lock()
	r.schemes[scheme] = h
	r.mu.Unlock()
}

func (r *rt) On(evt window.RuntimeEvent, fn func()) {
	r.mu.Lock()
	r.events[evt] = append(r.events[evt], fn)
	r.mu.Unlock()
}

func (r *rt) fire(evt window.RuntimeEvent) {
	r.mu.Lock()
	fns := append([]func(){}, r.events[evt]...)
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

var (
	taskMu   sync.Mutex
	taskSeq  uintptr
	taskMap  = map[uintptr]func(){}
	idleCB   uintptr
	idleOnce sync.Once
)

func initDispatch() {
	idleOnce.Do(func() {
		idleCB = purego.NewCallback(func(data uintptr) uintptr {
			taskMu.Lock()
			fn := taskMap[data]
			delete(taskMap, data)
			taskMu.Unlock()
			if fn != nil {
				fn()
			}
			return 0 // G_SOURCE_REMOVE — run once
		})
	})
}

func dispatchMain(fn func()) {
	taskMu.Lock()
	taskSeq++
	id := taskSeq
	taskMap[id] = fn
	taskMu.Unlock()
	gIdleAdd(idleCB, id)
}
