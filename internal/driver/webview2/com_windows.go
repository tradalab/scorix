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
// GC contract: pointer arguments are laundered through a []uintptr before the
// syscall, so the Go runtime's syscall pointer-pinning does NOT apply here. Any
// Go pointer passed by address must therefore either (a) point at a heap object
// (the GC never moves the heap, so the address stays valid), or (b) be a value
// the caller keeps live with runtime.KeepAlive AND that cannot move during the
// call. Do NOT pass the address of a stack-resident local directly — a stack
// growth between the conversion and the syscall would leave a stale address.
// For out-parameters use comCallOut, which heap-allocates the slot.
func comCall(this unsafe.Pointer, index int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(this)
	method := *(*uintptr)(unsafe.Add(vtbl, uintptr(index)*ptrSize))
	all := make([]uintptr, 1, len(args)+1)
	all[0] = uintptr(this)
	all = append(all, args...)
	ret, _, _ := syscall.SyscallN(method, all...)
	return ret
}

// comCallOut calls a method whose LAST argument is a pointer-sized out-parameter
// and returns what the COM method wrote. The out slot is heap-allocated so the
// GC can't move it across the (uintptr-laundered) syscall — passing a
// stack-local's address through comCall would be GC-unsafe. `extra` carries any
// arguments that precede the out-parameter.
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
// Every WebView2 handler we use exposes Invoke(this, a, b) — three pointer-sized
// args — so a single shared vtable backs the environment-completed,
// controller-completed and web-message-received handlers.
//
// The trampolines below convert the raw uintptr args the OS hands us into Go
// pointers. go vet's unsafeptr heuristic flags those conversions, but they are
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

// handlerSet tracks the COM callback objects owned by one window so they can be
// unpinned from handlerKeep when the window is disposed. Without this every
// per-window handler — and the *win/closures it captures — would stay pinned for
// the process lifetime, leaking a window's worth of state on every close.
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

// release unpins every tracked handler. Call ONLY after WebView2 has released
// its references (controller Closed + Released) so no late Invoke can race a
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
	// handlerInvoke is called by WebView2 (C) and runs arbitrary Go callbacks
	// (scheme handlers, message delivery). A panic unwinding across the C/COM
	// frame is undefined behavior / a hard crash, so contain it here and return
	// S_OK (0) — the callback's failure must not take down the message loop.
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
	// S_OK(0)=initialized, S_FALSE(1)=already initialized in the same mode (ok).
	// A negative HRESULT (notably RPC_E_CHANGED_MODE 0x80010106 — the thread was
	// already put into a different apartment by another component) means WebView2
	// may misbehave; surface it instead of silently ignoring.
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
