//go:build windows

package webview2

import (
	"fmt"
	goruntime "runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/logger"
)

const ptrSize = unsafe.Sizeof(uintptr(0))

// IUnknown vtable ordinals (shared by every COM interface).
const (
	iunknownQueryInterface = 0
	iunknownAddRef         = 1
	iunknownRelease        = 2
)

// comCall invokes COM vtable method `index` on `this` (the implicit first arg).
//
// GC contract: pointer args are laundered through a []uintptr, bypassing the
// runtime's syscall pointer-pinning. Any Go pointer passed by address must point
// at a heap object (GC never moves the heap) or be KeepAlive'd; never pass a
// stack local's address (a stack growth would stale it). Use comCallOut for
// out-params. A few sites pass &local and are safe only because escape analysis
// heap-promotes it (verified -gcflags=-m); switch them to comCallOut if that
// stops holding.
func comCall(this unsafe.Pointer, index int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(this)
	method := *(*uintptr)(unsafe.Add(vtbl, uintptr(index)*ptrSize))
	all := make([]uintptr, 1, len(args)+1)
	all[0] = uintptr(this)
	all = append(all, args...)
	ret, _, _ := syscall.SyscallN(method, all...)
	return ret
}

// comCallOut calls a method whose LAST arg is a pointer-sized out-param. The out
// slot is heap-allocated so the GC can't move it across the laundered syscall.
// `extra` carries args preceding the out-param.
func comCallOut(this unsafe.Pointer, index int, extra ...uintptr) (unsafe.Pointer, uintptr) {
	out := new(unsafe.Pointer) // heap: stable address across the call
	args := append(extra, uintptr(unsafe.Pointer(out)))
	ret := comCall(this, index, args...)
	goruntime.KeepAlive(out)
	return *out, ret
}

// comCallStr keeps the UTF-16 string alive across the syscall.
func comCallStr(this unsafe.Pointer, index int, s string, extra ...uintptr) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	args := append([]uintptr{uintptr(unsafe.Pointer(p))}, extra...)
	r := comCall(this, index, args...)
	goruntime.KeepAlive(p)
	return r
}

// ── COM callback object ──────────────────────────────────────────────────────
//
// Every WebView2 handler we use exposes Invoke(this, a, b), so one shared vtable
// backs the environment-completed, controller-completed and web-message handlers.
// The trampolines' uintptr->pointer conversions trip go vet's unsafeptr, but are
// safe: `this` is one of our pinned *handler objects and a/b are C-owned COM
// pointers — neither is a movable Go heap pointer.

type comVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	invoke         uintptr
}

type handler struct {
	vtbl *comVtbl // MUST be first field (COM object layout)
	fn   func(a, b unsafe.Pointer)
}

var (
	handlerVtbl = &comVtbl{
		queryInterface: windows.NewCallback(handlerQueryInterface),
		addRef:         windows.NewCallback(handlerAddRef),
		release:        windows.NewCallback(handlerRelease),
		invoke:         windows.NewCallback(handlerInvoke),
	}
	handlerKeep sync.Map // pin handlers against GC while the runtime holds them
)

func newHandler(fn func(a, b unsafe.Pointer)) *handler {
	h := &handler{vtbl: handlerVtbl, fn: fn}
	handlerKeep.Store(h, struct{}{})
	return h
}

// noopHandler satisfies COM methods that require a non-null completion handler
// (e.g. ExecuteScript) when we don't care about the result.
var noopHandler = newHandler(func(unsafe.Pointer, unsafe.Pointer) {})

// handlerSet tracks a window's COM callbacks so they can be unpinned from
// handlerKeep on dispose — otherwise each handler (and the *win it captures)
// stays pinned for the process lifetime, leaking per close.
type handlerSet struct {
	mu sync.Mutex
	hs []*handler
}

func (s *handlerSet) add(h *handler) *handler {
	s.mu.Lock()
	s.hs = append(s.hs, h)
	s.mu.Unlock()
	return h
}

// release unpins every tracked handler. Call ONLY after WebView2 has released its
// references (controller Closed + Released), else a late Invoke races a
// now-collectable Go object.
func (s *handlerSet) release() {
	s.mu.Lock()
	hs := s.hs
	s.hs = nil
	s.mu.Unlock()
	for _, h := range hs {
		handlerKeep.Delete(h)
	}
}

func handlerQueryInterface(this, _ /*iid*/, ppv uintptr) uintptr {
	if ppv != 0 {
		*(*uintptr)(unsafe.Pointer(ppv)) = this // permissive QI; sufficient for callbacks
	}
	return 0 // S_OK
}

func handlerAddRef(uintptr) uintptr  { return 1 }
func handlerRelease(uintptr) uintptr { return 1 }

func handlerInvoke(this, a, b uintptr) uintptr {
	// Runs Go callbacks (scheme handlers, message delivery) under a C/COM frame
	// where a panic is undefined behavior; contain it and return S_OK so one bad
	// callback can't kill the message loop.
	defer func() { _ = recover() }()
	h := (*handler)(unsafe.Pointer(this))
	if h.fn != nil {
		h.fn(unsafe.Pointer(a), unsafe.Pointer(b))
	}
	return 0
}

var (
	ole32              = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
)

// coInitSTA puts the calling (UI) thread into a single-threaded apartment,
// which WebView2 requires.
func coInitSTA() {
	const coinitApartmentThreaded = 2
	// S_OK/S_FALSE are fine. A negative HRESULT (notably RPC_E_CHANGED_MODE
	// 0x80010106 — another component already set a different apartment) means
	// WebView2 may misbehave; surface it rather than ignore.
	ret, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if int32(ret) < 0 {
		logger.Error(fmt.Sprintf("[webview2] CoInitializeEx(STA) failed: hr=0x%x (another component may have set a different COM apartment)", uint32(ret)))
	}
}

func coTaskMemFree(p unsafe.Pointer) {
	if p != nil {
		procCoTaskMemFree.Call(uintptr(p))
	}
}
