//go:build darwin

package wkwebview

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

// view hosts one WKWebView. JS -> Go arrives through the "scorix" script
// message handler (window.webkit.messageHandlers.scorix.postMessage); Go -> JS
// is evaluateJavaScript calling the bridge's global __scorix_receive.
type view struct {
	wk objc.ID // WKWebView

	mu    sync.Mutex
	onMsg func(raw []byte)
}

var viewByWK sync.Map // WKWebView objc.ID -> *view

func newView(r *rt, opts window.Options) (*view, error) {
	v := &view{}

	cfg := objc.ID(cls("WKWebViewConfiguration")).Send(sel("alloc")).Send(sel("init"))
	ucc := cfg.Send(sel("userContentController"))

	ucc.Send(sel("addScriptMessageHandler:name:"), newScriptHandler(), nsString("scorix"))

	// Bridge / init script, before any page script on every navigation.
	if opts.InitScript != "" {
		addUserScript(ucc, opts.InitScript)
	}

	// In-process custom schemes (scorix:// …) — must be configured before the
	// web view is created.
	r.mu.Lock()
	for scheme := range r.schemes {
		cfg.Send(sel("setURLSchemeHandler:forURLScheme:"), newSchemeHandler(), nsString(scheme))
	}
	r.mu.Unlock()

	rect := nsRect{Size: nsSize{W: float64(opts.Width), H: float64(opts.Height)}}
	wk := msgSendRectCfg(objc.ID(cls("WKWebView")).Send(sel("alloc")),
		sel("initWithFrame:configuration:"), rect, cfg)
	if wk == 0 {
		return nil, fmt.Errorf("wkwebview: WKWebView init failed")
	}
	wk.Send(sel("setAutoresizingMask:"), uint64(2|16)) // NSViewWidthSizable|NSViewHeightSizable

	v.wk = wk
	viewByWK.Store(wk, v)
	return v, nil
}

func addUserScript(ucc objc.ID, js string) {
	script := msgSendScript(objc.ID(cls("WKUserScript")).Send(sel("alloc")),
		sel("initWithSource:injectionTime:forMainFrameOnly:"),
		nsString(js), wkInjectAtDocumentStart, false)
	ucc.Send(sel("addUserScript:"), script)
}

func (v *view) Navigate(url string) {
	dispatchMain(func() {
		req := objc.ID(cls("NSURLRequest")).Send(sel("requestWithURL:"), nsURL(url))
		v.wk.Send(sel("loadRequest:"), req)
	})
}

func (v *view) LoadHTML(html string) {
	dispatchMain(func() {
		v.wk.Send(sel("loadHTMLString:baseURL:"), nsString(html), objc.ID(0))
	})
}

func (v *view) InitScript(js string) {
	dispatchMain(func() {
		ucc := v.wk.Send(sel("configuration")).Send(sel("userContentController"))
		addUserScript(ucc, js)
	})
}

func (v *view) Eval(js string) {
	dispatchMain(func() {
		// nil completion handler — fire and forget, like the other backends.
		v.wk.Send(sel("evaluateJavaScript:completionHandler:"), nsString(js), objc.ID(0))
	})
}

func (v *view) OpenDevTools() {
	// The Web Inspector needs the developerExtrasEnabled preference and a
	// private API to open programmatically; enable-by-preference is a TODO
	// for the hardware-validation pass.
}

func (v *view) OnMessage(fn func(raw []byte)) {
	v.mu.Lock()
	v.onMsg = fn
	v.mu.Unlock()
}

// PostMessage delivers a Go -> JS envelope by evaluating
// __scorix_receive("<json>") — the raw JSON is embedded as a JS string
// literal (json-escaped), and the bridge JSON.parses it.
func (v *view) PostMessage(raw []byte) error {
	lit, err := json.Marshal(string(raw))
	if err != nil {
		return err
	}
	v.Eval("window.__scorix_receive && window.__scorix_receive(" + string(lit) + ")")
	return nil
}

var scriptHandlerOnce sync.Once

func newScriptHandler() objc.ID {
	scriptHandlerOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixScriptHandler",
			cls("NSObject"),
			[]*objc.Protocol{objc.GetProtocol("WKScriptMessageHandler")},
			nil,
			[]objc.MethodDef{
				{
					Cmd: sel("userContentController:didReceiveScriptMessage:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, message objc.ID) {
						defer recoverCB("didReceiveScriptMessage")
						wk := message.Send(sel("webView"))
						body := message.Send(sel("body"))
						raw := nsStringToGo(body)
						if vv, ok := viewByWK.Load(wk); ok {
							v := vv.(*view)
							v.mu.Lock()
							fn := v.onMsg
							v.mu.Unlock()
							if fn != nil && raw != "" {
								fn([]byte(raw))
							}
						}
					},
				},
			},
		)
		if err != nil {
			panic(fmt.Sprintf("wkwebview: register script handler: %v", err))
		}
	})
	return objc.ID(cls("ScorixScriptHandler")).Send(sel("new"))
}

var schemeHandlerOnce sync.Once

func newSchemeHandler() objc.ID {
	schemeHandlerOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixSchemeHandler",
			cls("NSObject"),
			[]*objc.Protocol{objc.GetProtocol("WKURLSchemeHandler")},
			nil,
			[]objc.MethodDef{
				{
					Cmd: sel("webView:startURLSchemeTask:"),
					Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, task objc.ID) {
						defer recoverCB("startURLSchemeTask")
						serveSchemeTask(task)
					},
				},
				{
					Cmd: sel("webView:stopURLSchemeTask:"),
					Fn:  func(self objc.ID, _ objc.SEL, _ objc.ID, _ objc.ID) {},
				},
			},
		)
		if err != nil {
			panic(fmt.Sprintf("wkwebview: register scheme handler: %v", err))
		}
	})
	return objc.ID(cls("ScorixSchemeHandler")).Send(sel("new"))
}

func serveSchemeTask(task objc.ID) {
	activeMu.Lock()
	r := activeRT
	activeMu.Unlock()
	if r == nil {
		return
	}

	nsReq := task.Send(sel("request"))
	urlObj := nsReq.Send(sel("URL"))
	rawURL := nsStringToGo(urlObj.Send(sel("absoluteString")))
	scheme := nsStringToGo(urlObj.Send(sel("scheme")))

	r.mu.Lock()
	h := r.schemes[scheme]
	r.mu.Unlock()

	status, mime, body := 404, "text/plain", []byte("not found")
	if h != nil {
		resp := h(&webview.Request{Method: "GET", URL: rawURL})
		if resp != nil {
			if b, err := io.ReadAll(resp.Body); err == nil {
				body = b
			}
			status = resp.Status
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				mime = ct
			}
		}
	}
	_ = status // NSURLResponse carries no status; error pages still render their body

	urlResp := msgSendURLResp(objc.ID(cls("NSURLResponse")).Send(sel("alloc")),
		sel("initWithURL:MIMEType:expectedContentLength:textEncodingName:"),
		urlObj, nsString(mime), int64(len(body)), objc.ID(0))
	task.Send(sel("didReceiveResponse:"), urlResp)

	var ptr unsafe.Pointer
	if len(body) > 0 {
		ptr = unsafe.Pointer(&body[0])
	}
	// dataWithBytes:length: copies synchronously (unlike dataWithBytesNoCopy:) and
	// purego keeps ptr alive for the call, so passing Go memory is safe.
	data := msgSendBytesLen(objc.ID(cls("NSData")), sel("dataWithBytes:length:"), ptr, uint64(len(body)))
	task.Send(sel("didReceiveData:"), data)
	task.Send(sel("didFinish"))
}
