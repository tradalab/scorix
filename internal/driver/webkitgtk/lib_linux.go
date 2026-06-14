//go:build linux

// Package webkitgtk is the Linux native driver: GTK3 window + WebKitGTK via
// dlopen/purego, with runtime soname probing (4.1 → 4.0).
//
// EXPERIMENTAL: container-validated (make linuxcheck), not yet proven on real
// desktops (Wayland, GPU). Needs libgtk-3 + libwebkit2gtk-4.1 (or 4.0) at runtime.
package webkitgtk

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	gtkWindowToplevel int32 = 0

	webkitInjectAllFrames    int32 = 0 // WEBKIT_USER_CONTENT_INJECT_ALL_FRAMES
	webkitInjectDocumentStart int32 = 0 // WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START
)

var (
	libOnce sync.Once
	libErr  error

	gtkInitCheck         func(uintptr, uintptr) int32
	gtkMain              func()
	gtkMainQuit          func()
	gtkWindowNew         func(int32) uintptr
	gtkWindowSetTitle    func(uintptr, string)
	gtkWindowSetDefault  func(uintptr, int32, int32)
	gtkWindowResize      func(uintptr, int32, int32)
	gtkWindowMove        func(uintptr, int32, int32)
	gtkWindowGetSize     func(uintptr, *int32, *int32)
	gtkWindowGetPosition func(uintptr, *int32, *int32)
	gtkWindowPresent     func(uintptr)
	gtkWindowIconify     func(uintptr)
	gtkWindowDeiconify   func(uintptr)
	gtkWindowMaximize    func(uintptr)
	gtkWindowUnmaximize  func(uintptr)
	gtkWindowFullscreen  func(uintptr)
	gtkWindowUnfullscrn  func(uintptr)
	gtkWindowKeepAbove   func(uintptr, int32)
	gtkWindowSetDecor    func(uintptr, int32)
	gtkWindowSetResize   func(uintptr, int32)
	gtkWidgetShowAll     func(uintptr)
	gtkWidgetHide        func(uintptr)
	gtkWidgetShow        func(uintptr)
	gtkWidgetDestroy     func(uintptr)
	gtkContainerAdd      func(uintptr, uintptr)

	gSignalConnectData func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64
	gIdleAdd           func(uintptr, uintptr) uint32
	gFreeAddr          uintptr        // g_free as a GDestroyNotify (passed to g_memory_input_stream_new_from_data)
	gFree              func(uintptr)  // g_free as a callable (frees gchar* from jsc_value_to_string etc.)
	gMemdup            func(unsafe.Pointer, uint64) uintptr
	gMemStreamNew      func(uintptr, int64, uintptr) uintptr

	wkUcmNew            func() uintptr
	wkUcmRegisterScript func(uintptr, string)
	wkUcmAddScript      func(uintptr, uintptr)
	wkUserScriptNew     func(string, int32, int32, uintptr, uintptr) uintptr
	wkViewNewWithUcm    func(uintptr) uintptr
	wkViewLoadURI       func(uintptr, string)
	wkViewLoadHTML      func(uintptr, string, uintptr)
	wkViewRunJS         func(uintptr, string, uintptr, uintptr, uintptr)
	wkCtxDefault        func() uintptr
	wkCtxRegisterScheme func(uintptr, string, uintptr, uintptr, uintptr)
	wkSchemeReqGetURI   func(uintptr) uintptr
	wkSchemeReqFinish   func(uintptr, uintptr, int64, string)
	wkJSResultGetValue  func(uintptr) uintptr

	jscValueToString func(uintptr) uintptr
)

// webkitSonames in probe order — 4.1 (libsoup3) first, then 4.0 (libsoup2).
// GTK4/webkitgtk-6.0 is a different API generation: explicitly phase 2.
var webkitSonames = []string{
	"libwebkit2gtk-4.1.so.0",
	"libwebkit2gtk-4.0.so.37",
}

var jscSonames = []string{
	"libjavascriptcoregtk-4.1.so.0",
	"libjavascriptcoregtk-4.0.so.18",
}

func dlopenFirst(names []string) (uintptr, string, error) {
	flags := purego.RTLD_GLOBAL | purego.RTLD_NOW
	var lastErr error
	for _, n := range names {
		if h, err := purego.Dlopen(n, flags); err == nil {
			return h, n, nil
		} else {
			lastErr = err
		}
	}
	return 0, "", fmt.Errorf("webkitgtk: none of %v could be loaded (install the webkit2gtk runtime): %w", names, lastErr)
}

func initLibs() error {
	libOnce.Do(func() {
		flags := purego.RTLD_GLOBAL | purego.RTLD_NOW

		gtk, err := purego.Dlopen("libgtk-3.so.0", flags)
		if err != nil {
			libErr = fmt.Errorf("webkitgtk: load libgtk-3: %w", err)
			return
		}
		gobject, err := purego.Dlopen("libgobject-2.0.so.0", flags)
		if err != nil {
			libErr = err
			return
		}
		glib, err := purego.Dlopen("libglib-2.0.so.0", flags)
		if err != nil {
			libErr = err
			return
		}
		gio, err := purego.Dlopen("libgio-2.0.so.0", flags)
		if err != nil {
			libErr = err
			return
		}
		webkit, _, err := dlopenFirst(webkitSonames)
		if err != nil {
			libErr = err
			return
		}
		jsc, _, err := dlopenFirst(jscSonames)
		if err != nil {
			libErr = err
			return
		}

		purego.RegisterLibFunc(&gtkInitCheck, gtk, "gtk_init_check")
		purego.RegisterLibFunc(&gtkMain, gtk, "gtk_main")
		purego.RegisterLibFunc(&gtkMainQuit, gtk, "gtk_main_quit")
		purego.RegisterLibFunc(&gtkWindowNew, gtk, "gtk_window_new")
		purego.RegisterLibFunc(&gtkWindowSetTitle, gtk, "gtk_window_set_title")
		purego.RegisterLibFunc(&gtkWindowSetDefault, gtk, "gtk_window_set_default_size")
		purego.RegisterLibFunc(&gtkWindowResize, gtk, "gtk_window_resize")
		purego.RegisterLibFunc(&gtkWindowMove, gtk, "gtk_window_move")
		purego.RegisterLibFunc(&gtkWindowGetSize, gtk, "gtk_window_get_size")
		purego.RegisterLibFunc(&gtkWindowGetPosition, gtk, "gtk_window_get_position")
		purego.RegisterLibFunc(&gtkWindowPresent, gtk, "gtk_window_present")
		purego.RegisterLibFunc(&gtkWindowIconify, gtk, "gtk_window_iconify")
		purego.RegisterLibFunc(&gtkWindowDeiconify, gtk, "gtk_window_deiconify")
		purego.RegisterLibFunc(&gtkWindowMaximize, gtk, "gtk_window_maximize")
		purego.RegisterLibFunc(&gtkWindowUnmaximize, gtk, "gtk_window_unmaximize")
		purego.RegisterLibFunc(&gtkWindowFullscreen, gtk, "gtk_window_fullscreen")
		purego.RegisterLibFunc(&gtkWindowUnfullscrn, gtk, "gtk_window_unfullscreen")
		purego.RegisterLibFunc(&gtkWindowKeepAbove, gtk, "gtk_window_set_keep_above")
		purego.RegisterLibFunc(&gtkWindowSetDecor, gtk, "gtk_window_set_decorated")
		purego.RegisterLibFunc(&gtkWindowSetResize, gtk, "gtk_window_set_resizable")
		purego.RegisterLibFunc(&gtkWidgetShowAll, gtk, "gtk_widget_show_all")
		purego.RegisterLibFunc(&gtkWidgetHide, gtk, "gtk_widget_hide")
		purego.RegisterLibFunc(&gtkWidgetShow, gtk, "gtk_widget_show")
		purego.RegisterLibFunc(&gtkWidgetDestroy, gtk, "gtk_widget_destroy")
		purego.RegisterLibFunc(&gtkContainerAdd, gtk, "gtk_container_add")

		purego.RegisterLibFunc(&gSignalConnectData, gobject, "g_signal_connect_data")
		purego.RegisterLibFunc(&gIdleAdd, glib, "g_idle_add")
		if addr, err := purego.Dlsym(glib, "g_free"); err == nil {
			gFreeAddr = addr
		}
		purego.RegisterLibFunc(&gFree, glib, "g_free")
		// g_memdup2 (glib ≥ 2.68) with g_memdup fallback for older distros.
		if err := registerMemdup(glib); err != nil {
			libErr = err
			return
		}
		purego.RegisterLibFunc(&gMemStreamNew, gio, "g_memory_input_stream_new_from_data")

		purego.RegisterLibFunc(&wkUcmNew, webkit, "webkit_user_content_manager_new")
		purego.RegisterLibFunc(&wkUcmRegisterScript, webkit, "webkit_user_content_manager_register_script_message_handler")
		purego.RegisterLibFunc(&wkUcmAddScript, webkit, "webkit_user_content_manager_add_script")
		purego.RegisterLibFunc(&wkUserScriptNew, webkit, "webkit_user_script_new")
		purego.RegisterLibFunc(&wkViewNewWithUcm, webkit, "webkit_web_view_new_with_user_content_manager")
		purego.RegisterLibFunc(&wkViewLoadURI, webkit, "webkit_web_view_load_uri")
		purego.RegisterLibFunc(&wkViewLoadHTML, webkit, "webkit_web_view_load_html")
		purego.RegisterLibFunc(&wkViewRunJS, webkit, "webkit_web_view_run_javascript")
		purego.RegisterLibFunc(&wkCtxDefault, webkit, "webkit_web_context_get_default")
		purego.RegisterLibFunc(&wkCtxRegisterScheme, webkit, "webkit_web_context_register_uri_scheme")
		purego.RegisterLibFunc(&wkSchemeReqGetURI, webkit, "webkit_uri_scheme_request_get_uri")
		purego.RegisterLibFunc(&wkSchemeReqFinish, webkit, "webkit_uri_scheme_request_finish")
		purego.RegisterLibFunc(&wkJSResultGetValue, webkit, "webkit_javascript_result_get_js_value")

		purego.RegisterLibFunc(&jscValueToString, jsc, "jsc_value_to_string")
	})
	return libErr
}

func registerMemdup(glib uintptr) error {
	if _, err := purego.Dlsym(glib, "g_memdup2"); err == nil {
		purego.RegisterLibFunc(&gMemdup, glib, "g_memdup2")
		return nil
	}
	// Legacy g_memdup takes guint (32-bit) — wrap to the same Go signature.
	var legacy func(unsafe.Pointer, uint32) uintptr
	if _, err := purego.Dlsym(glib, "g_memdup"); err != nil {
		return fmt.Errorf("webkitgtk: neither g_memdup2 nor g_memdup found: %w", err)
	}
	purego.RegisterLibFunc(&legacy, glib, "g_memdup")
	gMemdup = func(p unsafe.Pointer, n uint64) uintptr { return legacy(p, uint32(n)) }
	return nil
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

func signalConnect(instance uintptr, signal string, cb uintptr, data uintptr) {
	gSignalConnectData(instance, signal, cb, data, 0, 0)
}
