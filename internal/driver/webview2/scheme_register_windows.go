//go:build windows

package webview2

// Custom-scheme registration: implements the WebView2 environment-options COM
// objects so a real custom scheme (scorix://) can be registered at environment
// creation. Unlike the rest of the driver (where WebView2 hands us objects),
// here WebView2 calls *into* our Go objects, so we implement full COM servers:
//
//   - ICoreWebView2EnvironmentOptions  (base, 8 getters/putters)
//   - ICoreWebView2EnvironmentOptions4 (GetCustomSchemeRegistrations)
//   - ICoreWebView2CustomSchemeRegistration (per scheme)
//
// IIDs and vtable order are taken verbatim from the WebView2 SDK header
// (Microsoft.Web.WebView2 / WebView2.h).

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// HRESULTs we return from COM server methods.
const (
	sOK          = 0
	eNoInterface = 0x80004002
	ePointer     = 0x80004003
	eOutOfMemory = 0x8007000E
)

// IIDs (verbatim from WebView2.h).
var (
	iidIUnknown     = mustGUID("{00000000-0000-0000-C000-000000000046}")
	iidEnvOptions   = mustGUID("{2FDE08A8-1E9A-4766-8C05-95A9CEB9D1C5}")
	iidEnvOptions4  = mustGUID("{AC52D13F-0D38-475A-9DCA-876580D6793E}")
	iidCustomScheme = mustGUID("{D60AC92C-37A6-4B26-A39E-95CFE59047BB}")
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

var procCoTaskMemAlloc = ole32.NewProc("CoTaskMemAlloc")

// coTaskMemDupUTF16 returns a CoTaskMemAlloc'd UTF-16 copy of s (WebView2 frees it).
func coTaskMemDupUTF16(s string) uintptr {
	u16, err := windows.UTF16FromString(s)
	if err != nil {
		return 0
	}
	p, _, _ := procCoTaskMemAlloc.Call(uintptr(len(u16) * 2))
	if p == 0 {
		return 0
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u16)), u16)
	return p
}

// ── vtable layouts (field order == COM method ordinals) ──────────────────────

type envOptionsBaseVtblT struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	getAddArgs     uintptr
	putAddArgs     uintptr
	getLanguage    uintptr
	putLanguage    uintptr
	getTargetVer   uintptr
	putTargetVer   uintptr
	getAllowSSO    uintptr
	putAllowSSO    uintptr
}

type envOptions4VtblT struct {
	queryInterface  uintptr
	addRef          uintptr
	release         uintptr
	getCustomScheme uintptr
	setCustomScheme uintptr
}

type schemeRegVtblT struct {
	queryInterface    uintptr
	addRef            uintptr
	release           uintptr
	getSchemeName     uintptr
	getTreatAsSecure  uintptr
	putTreatAsSecure  uintptr
	getAllowedOrigins uintptr
	setAllowedOrigins uintptr
	getHasAuthority   uintptr
	putHasAuthority   uintptr
}

var (
	envOptionsBaseVtbl = &envOptionsBaseVtblT{
		queryInterface: windows.NewCallback(envOptQI),
		addRef:         windows.NewCallback(handlerAddRef),
		release:        windows.NewCallback(handlerRelease),
		getAddArgs:     windows.NewCallback(optStringGetter),
		putAddArgs:     windows.NewCallback(optPutStub),
		getLanguage:    windows.NewCallback(optStringGetter),
		putLanguage:    windows.NewCallback(optPutStub),
		getTargetVer:   windows.NewCallback(optTargetVerGetter),
		putTargetVer:   windows.NewCallback(optPutStub),
		getAllowSSO:    windows.NewCallback(optBoolGetter),
		putAllowSSO:    windows.NewCallback(optPutStub),
	}
	envOptions4Vtbl = &envOptions4VtblT{
		queryInterface:  windows.NewCallback(envOptQI),
		addRef:          windows.NewCallback(handlerAddRef),
		release:         windows.NewCallback(handlerRelease),
		getCustomScheme: windows.NewCallback(opt4GetSchemes),
		setCustomScheme: windows.NewCallback(stub3),
	}
	schemeRegVtbl = &schemeRegVtblT{
		queryInterface:    windows.NewCallback(schemeQI),
		addRef:            windows.NewCallback(handlerAddRef),
		release:           windows.NewCallback(handlerRelease),
		getSchemeName:     windows.NewCallback(schemeGetName),
		getTreatAsSecure:  windows.NewCallback(schemeTrueGetter),
		putTreatAsSecure:  windows.NewCallback(optPutStub),
		getAllowedOrigins: windows.NewCallback(schemeGetAllowedOrigins),
		setAllowedOrigins: windows.NewCallback(stub3),
		getHasAuthority:   windows.NewCallback(schemeTrueGetter),
		putHasAuthority:   windows.NewCallback(optPutStub),
	}
)

type envOptions struct {
	regs      []*schemeReg
	baseHead  *envOptHead
	opts4Head *envOptHead
}

// envOptHead is one COM interface view of an envOptions (vtbl MUST be first).
type envOptHead struct {
	vtbl  uintptr
	owner *envOptions
}

// schemeReg is its own COM interface head (vtbl MUST be first).
type schemeReg struct {
	vtbl uintptr
	name string
}

// buildEnvOptions builds the COM options object registering each scheme name and
// returns the ICoreWebView2EnvironmentOptions* (base head) plus the GC-pin keys.
// Objects are pinned in handlerKeep so the GC can't move/collect them while
// WebView2 reads them during environment creation; the caller unpins the returned
// keys in dispose() (WebView2 consumes the options object during creation and
// doesn't retain it), so a scheme-registered window no longer leaks them for the
// process lifetime. NEEDS Windows runtime validation.
func buildEnvOptions(schemes []string) (unsafe.Pointer, []any) {
	owner := &envOptions{}
	var keep []any
	pin := func(o any) { handlerKeep.Store(o, struct{}{}); keep = append(keep, o) }
	for _, s := range schemes {
		sr := &schemeReg{vtbl: uintptr(unsafe.Pointer(schemeRegVtbl)), name: s}
		pin(sr)
		owner.regs = append(owner.regs, sr)
	}
	owner.baseHead = &envOptHead{vtbl: uintptr(unsafe.Pointer(envOptionsBaseVtbl)), owner: owner}
	owner.opts4Head = &envOptHead{vtbl: uintptr(unsafe.Pointer(envOptions4Vtbl)), owner: owner}
	pin(owner)
	pin(owner.baseHead)
	pin(owner.opts4Head)
	return unsafe.Pointer(owner.baseHead), keep
}

// ── trampolines (C boundary: uintptr args; see com_windows.go note) ──────────

func optStringGetter(_, out uintptr) uintptr {
	if out != 0 {
		*(*uintptr)(unsafe.Pointer(out)) = 0 // NULL => default
	}
	return sOK
}

func optTargetVerGetter(_, out uintptr) uintptr {
	if out != 0 {
		*(*uintptr)(unsafe.Pointer(out)) = coTaskMemDupUTF16("86.0.616.0")
	}
	return sOK
}

func optBoolGetter(_, out uintptr) uintptr {
	if out != 0 {
		*(*int32)(unsafe.Pointer(out)) = 0 // FALSE
	}
	return sOK
}

func optPutStub(_, _ uintptr) uintptr { return sOK }
func stub3(_, _, _ uintptr) uintptr   { return sOK }

func envOptQI(this, iid, ppv uintptr) uintptr {
	if ppv == 0 {
		return ePointer
	}
	owner := (*envOptHead)(unsafe.Pointer(this)).owner
	switch *(*windows.GUID)(unsafe.Pointer(iid)) {
	case iidIUnknown, iidEnvOptions:
		*(*uintptr)(unsafe.Pointer(ppv)) = uintptr(unsafe.Pointer(owner.baseHead))
		return sOK
	case iidEnvOptions4:
		*(*uintptr)(unsafe.Pointer(ppv)) = uintptr(unsafe.Pointer(owner.opts4Head))
		return sOK
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = 0
	return eNoInterface
}

func opt4GetSchemes(this, count, regs uintptr) uintptr {
	owner := (*envOptHead)(unsafe.Pointer(this)).owner
	n := len(owner.regs)
	if count != 0 {
		*(*uint32)(unsafe.Pointer(count)) = uint32(n)
	}
	if regs == 0 {
		return sOK
	}
	if n == 0 {
		*(*uintptr)(unsafe.Pointer(regs)) = 0
		return sOK
	}
	arr, _, _ := procCoTaskMemAlloc.Call(uintptr(n) * ptrSize)
	if arr == 0 {
		return eOutOfMemory
	}
	slice := unsafe.Slice((*uintptr)(unsafe.Pointer(arr)), n)
	for i, r := range owner.regs {
		slice[i] = uintptr(unsafe.Pointer(r))
	}
	*(*uintptr)(unsafe.Pointer(regs)) = arr
	return sOK
}

func schemeQI(this, iid, ppv uintptr) uintptr {
	if ppv == 0 {
		return ePointer
	}
	switch *(*windows.GUID)(unsafe.Pointer(iid)) {
	case iidIUnknown, iidCustomScheme:
		*(*uintptr)(unsafe.Pointer(ppv)) = this
		return sOK
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = 0
	return eNoInterface
}

func schemeGetName(this, out uintptr) uintptr {
	if out != 0 {
		r := (*schemeReg)(unsafe.Pointer(this))
		*(*uintptr)(unsafe.Pointer(out)) = coTaskMemDupUTF16(r.name)
	}
	return sOK
}

// schemeTrueGetter answers TRUE for TreatAsSecure and HasAuthorityComponent.
func schemeTrueGetter(_, out uintptr) uintptr {
	if out != 0 {
		*(*int32)(unsafe.Pointer(out)) = 1
	}
	return sOK
}

func schemeGetAllowedOrigins(_, count, out uintptr) uintptr {
	if count != 0 {
		*(*uint32)(unsafe.Pointer(count)) = 0
	}
	if out != 0 {
		*(*uintptr)(unsafe.Pointer(out)) = 0
	}
	return sOK
}
