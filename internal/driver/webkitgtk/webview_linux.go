//go:build linux

package webkitgtk

import (
	"encoding/json"
	"io"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

// view hosts one WebKitWebView. JS -> Go arrives through the "scorix" script
// message handler (window.webkit.messageHandlers.scorix.postMessage — the
// same bridge transport branch as WKWebView); Go -> JS runs JavaScript
// calling the bridge's global __scorix_receive.
type view struct {
	wk  uintptr // WebKitWebView*
	ucm uintptr // WebKitUserContentManager*

	mu    sync.Mutex
	onMsg func(raw []byte)
}

var (
	viewByUcm   sync.Map // ucm ptr -> *view
	scriptCB    uintptr
	scriptOnce  sync.Once
	schemeCB    uintptr
	schemeOnce  sync.Once
)

func newView(r *rt, opts window.Options) (*view, error) {
	v := &view{}

	v.ucm = wkUcmNew()
	viewByUcm.Store(v.ucm, v)

	scriptOnce.Do(func() {
		scriptCB = purego.NewCallback(func(ucm, jsResult, _ uintptr) uintptr {
			if vv, ok := viewByUcm.Load(ucm); ok {
				val := wkJSResultGetValue(jsResult)
				// jsc_value_to_string returns a gchar* the caller owns — read it
				// then g_free, or every JS->Go message leaks the string.
				cstr := jscValueToString(val)
				raw := goString(cstr)
				gFree(cstr)
				view := vv.(*view)
				view.mu.Lock()
				fn := view.onMsg
				view.mu.Unlock()
				if fn != nil && raw != "" {
					fn([]byte(raw))
				}
			}
			return 0
		})
	})
	wkUcmRegisterScript(v.ucm, "scorix")
	signalConnect(v.ucm, "script-message-received::scorix", scriptCB, 0)

	if opts.InitScript != "" {
		v.addUserScript(opts.InitScript)
	}

	v.wk = wkViewNewWithUcm(v.ucm)
	return v, nil
}

// registerSchemes installs every registered custom scheme on the default web
// context — once per process, before any view loads a scheme URL.
func registerSchemes(r *rt) {
	schemeOnce.Do(func() {
		schemeCB = purego.NewCallback(func(req, _ uintptr) uintptr {
			serveSchemeRequest(req)
			return 0
		})
		ctx := wkCtxDefault()
		r.mu.Lock()
		for scheme := range r.schemes {
			wkCtxRegisterScheme(ctx, scheme, schemeCB, 0, 0)
		}
		r.mu.Unlock()
	})
}

func serveSchemeRequest(req uintptr) {
	activeMu.Lock()
	r := activeRT
	activeMu.Unlock()
	if r == nil {
		return
	}

	rawURL := goString(wkSchemeReqGetURI(req))
	scheme := ""
	for i := 0; i < len(rawURL); i++ {
		if rawURL[i] == ':' {
			scheme = rawURL[:i]
			break
		}
	}

	r.mu.Lock()
	h := r.schemes[scheme]
	r.mu.Unlock()

	mime, body := "text/plain", []byte("not found")
	if h != nil {
		if resp := h(&webview.Request{Method: "GET", URL: rawURL}); resp != nil {
			if b, err := io.ReadAll(resp.Body); err == nil {
				body = b
			}
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				mime = ct
			}
		}
	}

	// Copy into C memory (g_memdup2) — WebKit consumes the stream async, so
	// handing it Go-managed bytes would race the GC. g_free owns the copy.
	var src unsafe.Pointer
	if len(body) > 0 {
		src = unsafe.Pointer(&body[0])
	}
	cbuf := gMemdup(src, uint64(len(body)))
	stream := gMemStreamNew(cbuf, int64(len(body)), gFreeAddr)
	wkSchemeReqFinish(req, stream, int64(len(body)), mime)
}

func (v *view) addUserScript(js string) {
	script := wkUserScriptNew(js, webkitInjectAllFrames, webkitInjectDocumentStart, 0, 0)
	wkUcmAddScript(v.ucm, script)
}

func (v *view) Navigate(url string) { dispatchMain(func() { wkViewLoadURI(v.wk, url) }) }

func (v *view) LoadHTML(html string) { dispatchMain(func() { wkViewLoadHTML(v.wk, html, 0) }) }

func (v *view) InitScript(js string) { dispatchMain(func() { v.addUserScript(js) }) }

func (v *view) Eval(js string) {
	dispatchMain(func() { wkViewRunJS(v.wk, js, 0, 0, 0) })
}

func (v *view) OpenDevTools() {
	// Needs the "enable-developer-extras" setting + webkit_web_inspector_show —
	// wired during hardware validation.
}

func (v *view) OnMessage(fn func(raw []byte)) {
	v.mu.Lock()
	v.onMsg = fn
	v.mu.Unlock()
}

// PostMessage delivers a Go -> JS envelope as __scorix_receive("<json>") with
// the payload embedded as a JSON-escaped JS string literal.
func (v *view) PostMessage(raw []byte) error {
	lit, err := json.Marshal(string(raw))
	if err != nil {
		return err
	}
	v.Eval("window.__scorix_receive && window.__scorix_receive(" + string(lit) + ")")
	return nil
}
