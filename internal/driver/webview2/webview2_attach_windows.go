//go:build windows

package webview2

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/webview"
)

// WebView2 COM method ordinals (vtable index, after the 3 IUnknown slots).
// Cross-checked against the wailsapp/go-webview2 vtable layouts.
const (
	// ICoreWebView2Environment
	envCreateController          = 3
	envCreateWebResourceResponse = 4
	// ICoreWebView2Controller
	ctrlPutIsVisible    = 4
	ctrlPutBounds       = 6
	ctrlClose           = 24
	ctrlGetCoreWebView2 = 25
	// ICoreWebView2
	cwvNavigate                = 5
	cwvNavigateToString        = 6
	cwvAddScriptOnDocCreated   = 27
	cwvExecuteScript           = 29
	cwvPostWebMessageString    = 33
	cwvAddWebMessageReceived   = 34
	cwvOpenDevToolsWindow      = 51
	cwvAddWebResourceRequested = 55
	cwvAddWebResourceReqFilter = 57
	// ICoreWebView2WebMessageReceivedEventArgs
	argsTryGetWebMessageAsString = 5
	// ICoreWebView2WebResourceRequestedEventArgs
	argsGetRequest  = 3
	argsPutResponse = 5
	// ICoreWebView2WebResourceRequest
	reqGetUri = 3
	// COREWEBVIEW2_WEB_RESOURCE_CONTEXT
	webResourceContextAll = 0
)

var (
	loaderOnce    sync.Once
	createEnvProc *windows.LazyProc
	loaderErr     error
)

// loadCreateEnv resolves CreateCoreWebView2EnvironmentWithOptions.
//
// It prefers the loader embedded in the binary (single-exe deployment),
// extracted once to a per-user cache, and falls back to a WebView2Loader.dll on
// PATH / next to the exe.
func loadCreateEnv() (*windows.LazyProc, error) {
	loaderOnce.Do(func() {
		name := "WebView2Loader.dll"
		if p, err := extractEmbeddedLoader(); err == nil && p != "" {
			name = p
		}
		dll := windows.NewLazyDLL(name)
		if err := dll.Load(); err != nil {
			loaderErr = fmt.Errorf("WebView2Loader.dll unavailable (need the Edge WebView2 Runtime installed): %w", err)
			return
		}
		createEnvProc = dll.NewProc("CreateCoreWebView2EnvironmentWithOptions")
	})
	return createEnvProc, loaderErr
}

// extractEmbeddedLoader writes the bundled loader to a per-user cache (once),
// returning its path, or "" if no loader is embedded.
func extractEmbeddedLoader() (string, error) {
	data := embeddedLoaderDLL()
	if len(data) == 0 {
		return "", nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "scorix", "webview2loader")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "WebView2Loader.dll")
	if fi, err := os.Stat(path); err != nil || fi.Size() != int64(len(data)) {
		tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	return path, nil
}

// createEnvironment starts the async WebView2 bring-up for hwnd. schemes are
// registered as custom schemes (e.g. "scorix"). onCore runs on the UI thread
// once the ICoreWebView2 + controller exist.
func createEnvironment(hwnd windows.Handle, userDataFolder string, schemes []string, track func(*handler) *handler, onCore func(core, controller, env unsafe.Pointer)) error {
	create, err := loadCreateEnv()
	if err != nil {
		return err
	}

	var optionsPtr uintptr
	if len(schemes) > 0 {
		optionsPtr = uintptr(buildEnvOptions(schemes))
	}

	// Held by the closures + handlerKeep, so safe from GC. track records them so
	// they're unpinned when the window is disposed (else they leak per window).
	envHandler := track(newHandler(func(_ /*hr*/, env unsafe.Pointer) {
		if env == nil {
			return
		}
		ctrlHandler := track(newHandler(func(_ /*hr*/, controller unsafe.Pointer) {
			if controller == nil {
				return
			}
			core, _ := comCallOut(controller, ctrlGetCoreWebView2)
			if core != nil {
				onCore(core, controller, env)
			}
		}))
		comCall(env, envCreateController, uintptr(hwnd), uintptr(unsafe.Pointer(ctrlHandler)))
	}))

	// Pass a real NULL (not a pointer to an empty UTF-16 string) when no folder
	// is configured, so WebView2 uses its default per-user location instead of
	// possibly rejecting "" or writing beside the exe.
	var udf *uint16
	var udfArg uintptr
	if userDataFolder != "" {
		udf, _ = windows.UTF16PtrFromString(userDataFolder)
		udfArg = uintptr(unsafe.Pointer(udf))
	}
	ret, _, _ := create.Call(
		0,                                   // browserExecutableFolder: use installed runtime
		udfArg,                              // userDataFolder (0 = default)
		optionsPtr,                          // environmentOptions (custom schemes)
		uintptr(unsafe.Pointer(envHandler)), // completed handler
	)
	goruntime.KeepAlive(udf)
	if int32(ret) < 0 {
		return fmt.Errorf("CreateCoreWebView2EnvironmentWithOptions: hr=0x%x", uint32(ret))
	}
	return nil
}

// ── webview2View: webview.View backed by an ICoreWebView2 (set async) ─────────

type webview2View struct {
	dispatch func(func()) // marshal a func onto the UI thread

	mu        sync.Mutex
	core      unsafe.Pointer
	onMsg     func(raw []byte)
	pending   [][]byte // Go->JS queued before the core is ready
	initJS    []string
	startURL  string
	startHTML string
}

var _ webview.View = (*webview2View)(nil)

func newView(dispatch func(func())) *webview2View {
	return &webview2View{dispatch: dispatch}
}

// ready wires the live core (UI thread): replays init scripts, the initial
// navigation, and any queued messages.
func (v *webview2View) ready(core unsafe.Pointer) {
	v.mu.Lock()
	v.core = core
	initJS := append([]string(nil), v.initJS...)
	pending := v.pending
	startURL, startHTML := v.startURL, v.startHTML
	v.pending, v.initJS = nil, nil
	v.mu.Unlock()

	for _, js := range initJS {
		comCallStr(core, cwvAddScriptOnDocCreated, js, uintptr(unsafe.Pointer(noopHandler)))
	}
	switch {
	case startHTML != "":
		comCallStr(core, cwvNavigateToString, startHTML)
	case startURL != "":
		comCallStr(core, cwvNavigate, startURL)
	}
	for _, raw := range pending {
		comCallStr(core, cwvPostWebMessageString, string(raw))
	}
}

// deliver pushes a JS->Go message to the registered sink (UI thread).
func (v *webview2View) deliver(raw []byte) {
	v.mu.Lock()
	fn := v.onMsg
	v.mu.Unlock()
	if fn != nil {
		fn(raw)
	}
}

func (v *webview2View) Navigate(url string) {
	v.dispatch(func() {
		v.mu.Lock()
		core := v.core
		if core == nil {
			v.startURL, v.startHTML = url, ""
		}
		v.mu.Unlock()
		if core != nil {
			comCallStr(core, cwvNavigate, url)
		}
	})
}

func (v *webview2View) LoadHTML(html string) {
	v.dispatch(func() {
		v.mu.Lock()
		core := v.core
		if core == nil {
			v.startHTML, v.startURL = html, ""
		}
		v.mu.Unlock()
		if core != nil {
			comCallStr(core, cwvNavigateToString, html)
		}
	})
}

func (v *webview2View) InitScript(js string) {
	v.mu.Lock()
	core := v.core
	if core == nil {
		v.initJS = append(v.initJS, js)
	}
	v.mu.Unlock()
	if core != nil {
		v.dispatch(func() { comCallStr(core, cwvAddScriptOnDocCreated, js, uintptr(unsafe.Pointer(noopHandler))) })
	}
}

func (v *webview2View) Eval(js string) {
	v.dispatch(func() {
		v.mu.Lock()
		core := v.core
		v.mu.Unlock()
		if core != nil {
			comCallStr(core, cwvExecuteScript, js, uintptr(unsafe.Pointer(noopHandler)))
		}
	})
}

func (v *webview2View) OpenDevTools() {
	v.dispatch(func() {
		v.mu.Lock()
		core := v.core
		v.mu.Unlock()
		if core != nil {
			comCall(core, cwvOpenDevToolsWindow)
		}
	})
}

func (v *webview2View) OnMessage(fn func(raw []byte)) {
	v.mu.Lock()
	v.onMsg = fn
	v.mu.Unlock()
}

func (v *webview2View) PostMessage(raw []byte) error {
	cp := append([]byte(nil), raw...)
	v.dispatch(func() {
		v.mu.Lock()
		core := v.core
		if core == nil {
			v.pending = append(v.pending, cp)
		}
		v.mu.Unlock()
		if core != nil {
			comCallStr(core, cwvPostWebMessageString, string(cp))
		}
	})
	return nil
}
