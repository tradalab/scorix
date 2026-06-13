//go:build darwin

package wkwebview

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego/objc"

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

func (driver) Name() string { return "wkwebview" }

func (driver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	if err := initObjC(); err != nil {
		return nil, fmt.Errorf("wkwebview: load frameworks: %w", err)
	}
	r := &rt{
		manager: &manager{byID: map[window.ID]*win{}},
		events:  map[window.RuntimeEvent][]func(){},
		schemes: map[string]webview.SchemeHandler{},
	}
	r.manager.rt = r

	// One runtime per process — same constraint as the webview2 backend (the
	// ObjC delegate classes are registered process-wide).
	activeMu.Lock()
	defer activeMu.Unlock()
	if activeRT != nil {
		return nil, fmt.Errorf("wkwebview: only one Runtime per process")
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
	app     objc.ID // NSApplication

	mu      sync.Mutex
	events  map[window.RuntimeEvent][]func()
	schemes map[string]webview.SchemeHandler
}

// Run starts NSApplication's event loop. Must be called from the main
// goroutine (AppKit requires the process main thread).
func (r *rt) Run() error {
	runtime.LockOSThread()

	r.app = objc.ID(cls("NSApplication")).Send(sel("sharedApplication"))
	r.app.Send(sel("setActivationPolicy:"), nsApplicationActivationPolicyRegular)

	// applicationDidFinishLaunching: → RuntimeReady (mirrors webview2).
	delegate := newAppDelegate(r)
	r.app.Send(sel("setDelegate:"), delegate)
	r.app.Send(sel("activateIgnoringOtherApps:"), true)

	r.app.Send(sel("run")) // blocks until stop:
	return nil
}

// Quit stops the event loop. -stop: only takes effect once an event is
// processed, so a no-op application-defined event is posted to wake the loop —
// the standard AppKit idiom for programmatic termination that RETURNS (unlike
// -terminate:, which exits the process and would skip Go-side shutdown).
func (r *rt) Quit() {
	r.fire(window.RuntimeBeforeQuit)
	dispatchMain(func() {
		r.app.Send(sel("stop:"), objc.ID(0))
		ev := msgSendEvent(objc.ID(cls("NSEvent")),
			sel("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:"),
			nsEventTypeApplicationDefined, nsPoint{}, 0, 0, 0, 0, 0, 0, 0)
		r.app.Send(sel("postEvent:atStart:"), ev, true)
	})
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

// ── NSApplicationDelegate (runtime-registered class) ─────────────────

var appDelegateOnce sync.Once

func newAppDelegate(r *rt) objc.ID {
	appDelegateOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixAppDelegate",
			cls("NSObject"),
			[]*objc.Protocol{objc.GetProtocol("NSApplicationDelegate")},
			nil,
			[]objc.MethodDef{
				{
					Cmd: sel("applicationDidFinishLaunching:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
						activeMu.Lock()
						rt := activeRT
						activeMu.Unlock()
						if rt != nil {
							rt.fire(window.RuntimeReady)
						}
					},
				},
				{
					// Keep running with zero windows is decided by US (quit on
					// last close happens in the window delegate), so AppKit's
					// own auto-termination must be off.
					Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) bool {
						return false
					},
				},
			},
		)
		if err != nil {
			panic(fmt.Sprintf("wkwebview: register app delegate: %v", err))
		}
	})
	return objc.ID(cls("ScorixAppDelegate")).Send(sel("new"))
}
