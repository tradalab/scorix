//go:build linux && !android

package webkitgtk

import (
	"net/url"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/tradalab/scorix/window"
)

const gdkActionCopy int32 = 1 << 1

type pendingDrop struct {
	w    *win
	x, y int32
}

var (
	dropWins    sync.Map
	dropPending sync.Map
	dropOnce    sync.Once
	dropMotion  uintptr
	dropDrop    uintptr
	dropData    uintptr
)

func filePathsFromURIs(uris []string) []string {
	seen := map[string]bool{}
	var paths []string
	for _, raw := range uris {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Scheme, "file") || u.Path == "" {
			continue
		}
		if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
			continue
		}
		if !seen[u.Path] {
			seen[u.Path] = true
			paths = append(paths, u.Path)
		}
	}
	return paths
}

var browserLinkTargets = map[string]bool{
	"text/x-moz-url":                     true,
	"_NETSCAPE_URL":                      true,
	"text/x-chromium-url":                true,
	"application/x-moz-file-promise-url": true,
}

func uriListAtom() uintptr {
	return gdkAtomIntern("text/uri-list", 0)
}

func isFileDragTargets(targets []string) bool {
	files := false
	for _, t := range targets {
		if browserLinkTargets[t] {
			return false
		}
		if t == "text/uri-list" {
			files = true
		}
	}
	return files
}

func dragTargetNames(ctx uintptr) []string {
	var names []string
	for l := gdkDragListTargets(ctx); l != 0; l = *(*uintptr)(unsafe.Pointer(l + unsafe.Sizeof(uintptr(0)))) {
		p := gdkAtomName(*(*uintptr)(unsafe.Pointer(l)))
		if p == 0 {
			continue
		}
		names = append(names, goString(p))
		gFree(p)
	}
	return names
}

func isExternalFileDrag(ctx uintptr) bool {
	if ctx == 0 || gtkDragSourceWidget(ctx) != 0 {
		return false
	}
	return isFileDragTargets(dragTargetNames(ctx))
}

func readURIs(selection uintptr) []string {
	arr := gtkSelectionGetURIs(selection)
	if arr == 0 {
		return nil
	}
	defer gStrfreev(arr)
	var uris []string
	for i := uintptr(0); ; i++ {
		p := *(*uintptr)(unsafe.Pointer(arr + i*unsafe.Sizeof(uintptr(0))))
		if p == 0 {
			return uris
		}
		uris = append(uris, goString(p))
	}
}

func initDropSignals() {
	dropOnce.Do(func() {
		dropMotion = purego.NewCallback(func(widget, ctx, _, _, time uintptr) uintptr {
			defer recoverCB("drag-motion")
			if !isExternalFileDrag(ctx) {
				return 0
			}
			gdkDragStatus(ctx, gdkActionCopy, uint32(time))
			return 1
		})
		dropDrop = purego.NewCallback(func(widget, ctx, x, y, time uintptr) uintptr {
			defer recoverCB("drag-drop")
			v, ok := dropWins.Load(widget)
			if !ok || !isExternalFileDrag(ctx) {
				return 0
			}
			dropPending.Store(widget, pendingDrop{w: v.(*win), x: int32(x), y: int32(y)})
			gtkDragGetData(widget, ctx, uriListAtom(), uint32(time))
			return 1
		})
		dropData = purego.NewCallback(func(widget, ctx, _, _, selection, _, time uintptr) uintptr {
			defer recoverCB("drag-data-received")
			p, ok := dropPending.LoadAndDelete(widget)
			if !ok {
				return 0
			}
			gSignalStopEmission(widget, "drag-data-received")
			files := filePathsFromURIs(readURIs(selection))
			gtkDragFinish(ctx, boolInt(len(files) > 0), 0, uint32(time))
			if len(files) > 0 {
				d := p.(pendingDrop)
				d.w.fireFileDrop(widget, d.x, d.y, files)
			}
			return 0
		})
	})
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func (w *win) acceptFileDrops() {
	initDropSignals()
	wk := w.view.wk
	dropWins.Store(wk, w)
	signalConnect(wk, "drag-motion", dropMotion, 0)
	signalConnect(wk, "drag-drop", dropDrop, 0)
	signalConnect(wk, "drag-data-received", dropData, 0)
}

func (w *win) forgetFileDrops() {
	dropWins.Delete(w.view.wk)
	dropPending.Delete(w.view.wk)
}

func (w *win) fireFileDrop(widget uintptr, x, y int32, files []string) {
	wx, wy := x, y
	if gtkWidgetTranslate(widget, w.gw, x, y, &wx, &wy) == 0 {
		wx, wy = x, y
	}
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[window.EventFileDrop]...)
	w.mu.Unlock()
	data := window.EventData{Window: w.id, X: int(wx), Y: int(wy), Files: files}
	for _, fn := range fns {
		fn(data)
	}
}
