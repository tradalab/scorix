//go:build darwin && !ios

package wkwebview

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/window"
)

const (
	nsDragOperationNone uint64 = 0
	nsDragOperationCopy uint64 = 1
)

var (
	dropViewOnce sync.Once
	dropWins     sync.Map
)

func dropWebViewClass() objc.Class {
	dropViewOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixDropWebView",
			cls("WKWebView"),
			nil,
			nil,
			[]objc.MethodDef{
				{
					Cmd: sel("draggingEntered:"),
					Fn: func(self objc.ID, cmd objc.SEL, info objc.ID) uint64 {
						defer recoverCB("draggingEntered:")
						if isExternalFileDrag(info) {
							return nsDragOperationCopy
						}
						return objc.SendSuper[uint64](self, cmd, info)
					},
				},
				{
					Cmd: sel("draggingUpdated:"),
					Fn: func(self objc.ID, cmd objc.SEL, info objc.ID) uint64 {
						defer recoverCB("draggingUpdated:")
						if isExternalFileDrag(info) {
							return nsDragOperationCopy
						}
						return objc.SendSuper[uint64](self, cmd, info)
					},
				},
				{
					Cmd: sel("performDragOperation:"),
					Fn: func(self objc.ID, cmd objc.SEL, info objc.ID) bool {
						defer recoverCB("performDragOperation:")
						if !isExternalFileDrag(info) {
							return objc.SendSuper[bool](self, cmd, info)
						}
						files := droppedPaths(info)
						if v, ok := dropWins.Load(self); ok && len(files) > 0 {
							x, y := dropPoint(self, info)
							v.(*win).fireFileDrop(x, y, files)
						}
						return len(files) > 0
					},
				},
			},
		)
		if err != nil {
			panic(fmt.Sprintf("wkwebview: register drop web view: %v", err))
		}
	})
	return cls("ScorixDropWebView")
}

func fileURLReadingOptions() (classes, options objc.ID) {
	classes = objc.ID(cls("NSArray")).Send(sel("arrayWithObject:"), objc.ID(cls("NSURL")))
	yes := objc.ID(cls("NSNumber")).Send(sel("numberWithBool:"), true)
	options = objc.ID(cls("NSDictionary")).Send(sel("dictionaryWithObject:forKey:"), yes, nsString("NSPasteboardURLReadingFileURLsOnlyKey"))
	return classes, options
}

func isExternalFileDrag(info objc.ID) bool {
	if info == 0 || info.Send(sel("draggingSource")) != 0 {
		return false
	}
	classes, options := fileURLReadingOptions()
	return objc.Send[bool](info.Send(sel("draggingPasteboard")), sel("canReadObjectForClasses:options:"), classes, options)
}

func droppedPaths(info objc.ID) []string {
	classes, options := fileURLReadingOptions()
	urls := info.Send(sel("draggingPasteboard")).Send(sel("readObjectsForClasses:options:"), classes, options)
	if urls == 0 {
		return nil
	}
	n := objc.Send[uint64](urls, sel("count"))
	paths := make([]string, 0, n)
	for i := uint64(0); i < n; i++ {
		if p := nsStringToGo(urls.Send(sel("objectAtIndex:"), i).Send(sel("path"))); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func dropPoint(wk, info objc.ID) (int, int) {
	var loc nsPoint
	if v := info.Send(sel("valueForKey:"), nsString("draggingLocation")); v != 0 {
		msgSendGetValue(v, sel("getValue:size:"), unsafe.Pointer(&loc), uint64(unsafe.Sizeof(loc)))
	}
	frame, _ := rectOf(wk, "frame")
	return int(loc.X), int(frame.Size.H - loc.Y)
}

func (w *win) acceptFileDrops() {
	dropWins.Store(w.view.wk, w)
	w.view.wk.Send(sel("registerForDraggedTypes:"),
		objc.ID(cls("NSArray")).Send(sel("arrayWithObject:"), nsString("public.file-url")))
}

func (w *win) forgetFileDrops() {
	dropWins.Delete(w.view.wk)
}

func (w *win) fireFileDrop(x, y int, files []string) {
	w.mu.Lock()
	fns := append([]func(window.EventData){}, w.events[window.EventFileDrop]...)
	w.mu.Unlock()
	data := window.EventData{Window: w.id, X: x, Y: y, Files: files}
	for _, fn := range fns {
		fn(data)
	}
}
