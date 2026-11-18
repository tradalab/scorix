//go:build ios && cgo

package ioswebview

/*
#include <stdlib.h>
#include "host_ios.h"
*/
import "C"

import (
	"unsafe"

	"github.com/tradalab/scorix/internal/driver/mobilebridge"
	"github.com/tradalab/scorix/window"
)

func New() window.Driver {
	return mobilebridge.New("ioswebview", mobilebridge.URLs{ToWeb: sameURL, FromWeb: fromWebURL, Origin: originOf}, toHost)
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

//export scorixSetHost
func scorixSetHost(fn C.scorix_host_fn) { C.scorix_keep_host(fn) }

//export scorixStart
func scorixStart(env *C.char, n C.int) {
	mobilebridge.Start(C.GoBytes(unsafe.Pointer(env), n), mainMain)
}

//export scorixMessage
func scorixMessage(data *C.char, n C.int) {
	mobilebridge.Message(C.GoBytes(unsafe.Pointer(data), n))
}

//export scorixCallReply
func scorixCallReply(data *C.char, n C.int) {
	mobilebridge.CallReply(C.GoBytes(unsafe.Pointer(data), n))
}

//export scorixRunTasks
func scorixRunTasks() { mobilebridge.RunTasks() }

//export scorixOpenURL
func scorixOpenURL(data *C.char, n C.int) {
	mobilebridge.OpenURL(string(C.GoBytes(unsafe.Pointer(data), n)))
}

//export scorixKeepsOut
func scorixKeepsOut(data *C.char, n C.int, clicked C.int) C.int {
	if mobilebridge.KeepsOut(string(C.GoBytes(unsafe.Pointer(data), n)), clicked != 0) {
		return 1
	}
	return 0
}

//export scorixLifecycle
func scorixLifecycle(code C.int) { mobilebridge.Lifecycle(int(code)) }

//export scorixRequest
func scorixRequest(data *C.char, n C.int, outLen *C.int) unsafe.Pointer {
	out := mobilebridge.ServeRequest(C.GoBytes(unsafe.Pointer(data), n))
	*outLen = C.int(len(out))
	p := C.malloc(C.size_t(len(out)))
	copy(unsafe.Slice((*byte)(p), len(out)), out)
	return p
}

//export scorixFree
func scorixFree(p unsafe.Pointer) { C.free(p) }
