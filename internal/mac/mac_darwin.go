//go:build darwin

// Package mac is the Objective-C runtime access shared by the macOS modules
// (status bar, notifications) through purego, with no CGO.
//
// It deliberately does NOT serve internal/driver/wkwebview, which still carries
// its own private copy of these helpers. Merging the two means editing ABI code
// that has never run on real hardware; do it once a Mac is available, not before.
package mac

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/tradalab/scorix/logger"
)

// Size is NSSize/CGSize on 64-bit.
type Size struct{ W, H float64 }

var (
	once    sync.Once
	initErr error

	mainQueue    uintptr
	dispatchAsyn func(q, ctx, work uintptr)
	workCallback uintptr

	// objc_msgSend bound under several Go signatures, which is purego's
	// documented way to pass arguments the variadic objc.Send cannot carry:
	// floats, structs and explicit widths.
	sendStr      func(objc.ID, objc.SEL, string) objc.ID
	sendFloat    func(objc.ID, objc.SEL, float64) objc.ID
	sendSize     func(objc.ID, objc.SEL, Size)
	sendBytesLen func(objc.ID, objc.SEL, unsafe.Pointer, uint64) objc.ID
	sendOptsBlk  func(objc.ID, objc.SEL, uint64, uintptr) objc.ID

	poolPush func() uintptr
	poolPop  func(uintptr)
)

// Init is safe from any goroutine and any number of times.
func Init() error {
	once.Do(func() {
		flags := purego.RTLD_GLOBAL | purego.RTLD_NOW
		if _, err := purego.Dlopen("/System/Library/Frameworks/Cocoa.framework/Cocoa", flags); err != nil {
			initErr = err
			return
		}
		// Not fatal: an app that only wants a status item must still start when
		// notifications are unavailable.
		if _, err := purego.Dlopen(
			"/System/Library/Frameworks/UserNotifications.framework/UserNotifications", flags); err != nil {
			logger.Warn("mac: UserNotifications not loaded, notifications fall back", "err", err)
		}
		libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", flags)
		if err != nil {
			initErr = err
			return
		}
		purego.RegisterLibFunc(&sendStr, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&sendFloat, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&sendSize, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&sendBytesLen, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&sendOptsBlk, libobjc, "objc_msgSend")
		purego.RegisterLibFunc(&poolPush, libobjc, "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&poolPop, libobjc, "objc_autoreleasePoolPop")

		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", flags)
		if err != nil {
			initErr = err
			return
		}
		purego.RegisterLibFunc(&dispatchAsyn, libSystem, "dispatch_async_f")
		// dispatch_get_main_queue() is a macro over _dispatch_main_q; the queue IS
		// the symbol's address.
		mq, err := purego.Dlsym(libSystem, "_dispatch_main_q")
		if err != nil {
			initErr = err
			return
		}
		mainQueue = mq
		workCallback = purego.NewCallback(func(ctx uintptr) uintptr {
			defer Recover("dispatchTask")
			runTask(ctx)
			return 0
		})
	})
	return initErr
}

var (
	taskMu  sync.Mutex
	taskSeq uintptr
	tasks   = map[uintptr]func(){}
)

// DispatchMain queues fn on the main run loop. Every AppKit call must go through
// it: a status item touched off the main thread corrupts the menu bar rather
// than failing. The queue drains only once NSApplication runs, so work posted
// before launch is held, not lost.
//
// It initialises on demand because IPC reaches it from OnLoad, before OnStart
// has called Init. Dropping the work is the right failure: the alternative is a
// nil call that kills the process on a JS message.
func DispatchMain(fn func()) {
	if err := Init(); err != nil {
		logger.Error("mac: cannot reach the main queue, work dropped", "err", err)
		return
	}
	taskMu.Lock()
	taskSeq++
	id := taskSeq
	tasks[id] = fn
	taskMu.Unlock()
	dispatchAsyn(mainQueue, id, workCallback)
}

func runTask(id uintptr) {
	taskMu.Lock()
	fn := tasks[id]
	delete(tasks, id)
	taskMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Recover contains a panic before it unwinds through the Objective-C runtime,
// which is undefined behavior. Every callback running app code must defer it.
func Recover(where string) {
	if r := recover(); r != nil {
		logger.Error("mac: recovered panic in C callback", "where", where, "panic", r)
	}
}

// withPool runs fn inside an autorelease pool. A Go goroutine is not an AppKit
// thread and carries no pool of its own, so any autoreleased object created off
// the main queue leaks and the runtime logs about it.
func withPool(fn func()) {
	if poolPush == nil || poolPop == nil {
		fn()
		return
	}
	p := poolPush()
	defer poolPop(p)
	fn()
}

func Sel(name string) objc.SEL { return objc.RegisterName(name) }

// Class is the class object, the receiver for a +method.
func Class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func NSString(s string) objc.ID {
	return sendStr(Class("NSString"), Sel("stringWithUTF8String:"), s)
}

func GoString(str objc.ID) string {
	if str == 0 {
		return ""
	}
	p := objc.Send[uintptr](str, Sel("UTF8String"))
	if p == 0 {
		return ""
	}
	var n int
	for *(*byte)(unsafe.Pointer(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
}

// NSData copies b, so the Go slice is free to move afterwards.
func NSData(b []byte) objc.ID {
	if len(b) == 0 {
		return 0
	}
	return sendBytesLen(Class("NSData"), Sel("dataWithBytes:length:"),
		unsafe.Pointer(&b[0]), uint64(len(b)))
}

func SendFloat(id objc.ID, s objc.SEL, f float64) objc.ID { return sendFloat(id, s, f) }

func SendSize(id objc.ID, s objc.SEL, sz Size) { sendSize(id, s, sz) }

// SendOptionsBlock is the shape of requestAuthorizationWithOptions:completionHandler:.
func SendOptionsBlock(id objc.ID, s objc.SEL, opts uint64, block uintptr) objc.ID {
	return sendOptsBlk(id, s, opts, block)
}

var (
	bundleOnce sync.Once
	bundleID   string
)

// BundleID is empty for a binary that is not inside a .app. Notification APIs
// raise an Objective-C exception rather than returning an error there, and an
// exception crossing purego is a crash, so callers must check first.
//
// Computed once: it is asked on every Notify from an ordinary goroutine, where
// each call would otherwise strand an NSString.
func BundleID() string {
	bundleOnce.Do(func() {
		withPool(func() {
			b := Class("NSBundle").Send(Sel("mainBundle"))
			if b == 0 {
				return
			}
			bundleID = GoString(b.Send(Sel("bundleIdentifier")))
		})
	})
	return bundleID
}
