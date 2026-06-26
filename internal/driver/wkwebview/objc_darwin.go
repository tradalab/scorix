//go:build darwin

// Package wkwebview is the macOS native driver: AppKit window + WKWebView via
// the Objective-C runtime through purego.
//
// EXPERIMENTAL: not yet hardware-validated — objc_msgSend ABI paths (NSRect
// struct args on amd64) need on-device checks.
package wkwebview

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// 64-bit CoreGraphics geometry (NSRect == CGRect on 64-bit).
type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

const (
	nsWindowStyleTitled         uint64 = 1 << 0
	nsWindowStyleClosable       uint64 = 1 << 1
	nsWindowStyleMiniaturizable uint64 = 1 << 2
	nsWindowStyleResizable      uint64 = 1 << 3

	nsBackingStoreBuffered uint64 = 2

	nsApplicationActivationPolicyRegular int64 = 0

	wkInjectAtDocumentStart int64 = 0

	nsEventTypeApplicationDefined uint64 = 15

	// WKMediaCaptureType: the kind getUserMedia({audio}) requests.
	wkMediaCaptureMicrophone int64 = 1
	// WKPermissionDecision handed to the media-capture decision handler.
	wkPermissionGrant int64 = 1
	wkPermissionDeny  int64 = 2
)

var (
	objcOnce sync.Once
	objcErr  error

	mainQueue      uintptr
	dispatchAsyncF func(q uintptr, ctx uintptr, work uintptr)
	workCallback   uintptr

	// Typed objc_msgSend bindings for signatures the variadic objc.Send can't
	// carry reliably (struct, float and mixed-width arguments). Binding the
	// same symbol under several Go signatures is purego's documented pattern.
	msgSendRectStyle func(objc.ID, objc.SEL, nsRect, uint64, uint64, bool) objc.ID // initWithContentRect:styleMask:backing:defer:
	msgSendRectCfg   func(objc.ID, objc.SEL, nsRect, objc.ID) objc.ID              // initWithFrame:configuration:
	msgSendStr       func(objc.ID, objc.SEL, string) objc.ID                       // stringWithUTF8String:
	msgSendBytesLen  func(objc.ID, objc.SEL, unsafe.Pointer, uint64) objc.ID       // dataWithBytes:length:
	msgSendScript    func(objc.ID, objc.SEL, objc.ID, int64, bool) objc.ID         // initWithSource:injectionTime:forMainFrameOnly:
	msgSendURLResp   func(objc.ID, objc.SEL, objc.ID, objc.ID, int64, objc.ID) objc.ID
	msgSendEvent     func(objc.ID, objc.SEL, uint64, nsPoint, uint64, float64, int64, objc.ID, int16, int64, int64) objc.ID
	msgSendSetFrame  func(objc.ID, objc.SEL, nsRect, bool) // setFrame:display:
)

func initObjC() error {
	objcOnce.Do(func() {
		flags := purego.RTLD_GLOBAL | purego.RTLD_NOW
		if _, err := purego.Dlopen("/System/Library/Frameworks/Cocoa.framework/Cocoa", flags); err != nil {
			objcErr = err
			return
		}
		if _, err := purego.Dlopen("/System/Library/Frameworks/WebKit.framework/WebKit", flags); err != nil {
			objcErr = err
			return
		}
		libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", flags)
		if err != nil {
			objcErr = err
			return
		}
		purego.RegisterLibFunc(&msgSendRectStyle, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendRectCfg, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendStr, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendBytesLen, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendScript, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendURLResp, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendEvent, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&msgSendSetFrame, libobjc, "objc_msgSend")

		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", flags)
		if err != nil {
			objcErr = err
			return
		}
		purego.RegisterLibFunc(&dispatchAsyncF, libSystem, "dispatch_async_f")
		// dispatch_get_main_queue() is a macro over the _dispatch_main_q symbol;
		// the queue IS the symbol's address.
		mq, err := purego.Dlsym(libSystem, "_dispatch_main_q")
		if err != nil {
			objcErr = err
			return
		}
		mainQueue = mq

		workCallback = purego.NewCallback(func(ctx uintptr) uintptr {
			defer recoverCB("dispatchTask")
			runDispatchTask(ctx)
			return 0
		})
	})
	return objcErr
}

var (
	taskMu  sync.Mutex
	taskSeq uintptr
	taskMap = map[uintptr]func(){}
)

func dispatchMain(fn func()) {
	taskMu.Lock()
	taskSeq++
	id := taskSeq
	taskMap[id] = fn
	taskMu.Unlock()
	dispatchAsyncF(mainQueue, id, workCallback)
}

func runDispatchTask(id uintptr) {
	taskMu.Lock()
	fn := taskMap[id]
	delete(taskMap, id)
	taskMu.Unlock()
	if fn != nil {
		fn()
	}
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func cls(name string) objc.Class { return objc.GetClass(name) }

// nsString builds an autorelease-pool-owned NSString from a Go string.
func nsString(s string) objc.ID {
	return msgSendStr(objc.ID(cls("NSString")), sel("stringWithUTF8String:"), s)
}

func goString(p uintptr) string {
	if p == 0 {
		return ""
	}
	var n int
	for *(*byte)(unsafe.Pointer(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
}

func nsStringToGo(str objc.ID) string {
	if str == 0 {
		return ""
	}
	return goString(objc.Send[uintptr](str, sel("UTF8String")))
}

func nsURL(u string) objc.ID {
	return objc.ID(cls("NSURL")).Send(sel("URLWithString:"), nsString(u))
}

// invokeCaptureDecision calls a WKPermissionDecisionHandler block,
// void (^)(WKPermissionDecision). A block's invoke fn pointer sits at offset
// 16 on 64-bit (isa 0, flags 8, reserved 12); call it as invoke(block, arg).
// Called synchronously inside the delegate, so no Block_copy is needed.
func invokeCaptureDecision(handler objc.ID, decision int64) {
	if handler == 0 {
		return
	}
	invoke := *(*uintptr)(unsafe.Pointer(uintptr(handler) + 16))
	purego.SyscallN(invoke, uintptr(handler), uintptr(decision))
}
