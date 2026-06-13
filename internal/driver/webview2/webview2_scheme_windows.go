//go:build windows

package webview2

import (
	"io"
	"net/http"
	goruntime "runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/webview"
)

// Registered custom schemes use HasAuthorityComponent=true, so asset URLs take
// the form scheme://<host>/path (apps use a conventional "app" host, e.g.
// scorix://app/index.html).

var shlwapi = windows.NewLazySystemDLL("shlwapi.dll")
var procSHCreateMemStream = shlwapi.NewProc("SHCreateMemStream")

// wireScheme registers an in-process asset handler for `scheme`. The returned
// COM callback must be tracked for unpinning on dispose.
func wireScheme(core, env unsafe.Pointer, scheme string, h webview.SchemeHandler) *handler {
	filter := scheme + "://*"
	comCallStr(core, cwvAddWebResourceReqFilter, filter, webResourceContextAll)

	handler := newHandler(func(_ /*sender*/, args unsafe.Pointer) {
		serveRequest(env, args, h)
	})
	token := new(int64) // heap: GC-stable address for the out-param
	comCall(core, cwvAddWebResourceRequested, uintptr(unsafe.Pointer(handler)), uintptr(unsafe.Pointer(token)))
	return handler
}

// serveRequest answers one WebResourceRequested event from the handler (UI thread).
func serveRequest(env, args unsafe.Pointer, h webview.SchemeHandler) {
	req, _ := comCallOut(args, argsGetRequest)
	if req == nil {
		return
	}
	uriPtr, _ := comCallOut(req, reqGetUri)
	comCall(req, iunknownRelease)
	if uriPtr == nil {
		return
	}
	uri := windows.UTF16PtrToString((*uint16)(uriPtr))
	coTaskMemFree(uriPtr)

	resp := h(&webview.Request{Method: "GET", URL: uri})
	if resp == nil {
		return
	}
	var body []byte
	if resp.Body != nil {
		body, _ = io.ReadAll(resp.Body)
	}

	stream := shCreateMemStream(body)
	if stream == nil {
		return
	}
	defer comCall(stream, iunknownRelease)

	reason := http.StatusText(resp.Status)
	if reason == "" {
		reason = "OK"
	}
	reasonPtr, _ := windows.UTF16PtrFromString(reason)
	headersPtr, _ := windows.UTF16PtrFromString(buildHeaders(resp.Header))

	var wvResp unsafe.Pointer
	comCall(env, envCreateWebResourceResponse,
		uintptr(stream),
		uintptr(resp.Status),
		uintptr(unsafe.Pointer(reasonPtr)),
		uintptr(unsafe.Pointer(headersPtr)),
		uintptr(unsafe.Pointer(&wvResp)),
	)
	goruntime.KeepAlive(reasonPtr)
	goruntime.KeepAlive(headersPtr)
	if wvResp == nil {
		return
	}
	defer comCall(wvResp, iunknownRelease)

	comCall(args, argsPutResponse, uintptr(wvResp))
}

// shCreateMemStream wraps SHCreateMemStream, which copies the buffer into a new
// IStream (refcount 1, owned by the caller).
func shCreateMemStream(data []byte) unsafe.Pointer {
	var p, n uintptr
	if len(data) > 0 {
		p = uintptr(unsafe.Pointer(&data[0]))
		n = uintptr(len(data))
	}
	r, _, _ := procSHCreateMemStream.Call(p, n)
	goruntime.KeepAlive(data)
	return unsafe.Pointer(r)
}

func buildHeaders(hdr http.Header) string {
	var b strings.Builder
	for k, vs := range hdr {
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}
