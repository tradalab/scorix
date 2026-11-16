//go:build linux && !android

package webkitgtk

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	wkSnapshotRegionVisible uintptr = 0
	wkSnapshotOptionsNone   uintptr = 0
	cairoStatusSuccess      uintptr = 0
	cairoStatusWriteError   uintptr = 11
)

var (
	captureOnce    sync.Once
	captureInitErr error
	snapshotDoneCB uintptr
	pngWriteCB     uintptr

	wkGetSnapshot       func(view, region, options, cancellable, cb, data uintptr)
	wkGetSnapshotFinish func(view, res uintptr, gerr *uintptr) uintptr
	cairoWriteToPNG     func(surface, write, closure uintptr) uintptr
	cairoSurfaceDestroy func(surface uintptr)
	cairoStatusToString func(status uintptr) string
	gErrorFree          func(gerr uintptr)

	captureMu  sync.Mutex
	captureSeq uintptr
	captures   = map[uintptr]*pendingCapture{}
)

type pendingCapture struct {
	png []byte
	ch  chan captureResult
}

type captureResult struct {
	png []byte
	err error
}

func initCapture() error {
	captureOnce.Do(func() {
		if err := initLibs(); err != nil {
			captureInitErr = err
			return
		}
		flags := purego.RTLD_GLOBAL | purego.RTLD_NOW
		webkit, _, err := dlopenFirst(webkitSonames)
		if err != nil {
			captureInitErr = err
			return
		}
		cairo, err := purego.Dlopen("libcairo.so.2", flags)
		if err != nil {
			captureInitErr = fmt.Errorf("webkitgtk: load libcairo: %w", err)
			return
		}
		glib, err := purego.Dlopen("libglib-2.0.so.0", flags)
		if err != nil {
			captureInitErr = err
			return
		}
		purego.RegisterLibFunc(&wkGetSnapshot, webkit, "webkit_web_view_get_snapshot")
		purego.RegisterLibFunc(&wkGetSnapshotFinish, webkit, "webkit_web_view_get_snapshot_finish")
		purego.RegisterLibFunc(&cairoWriteToPNG, cairo, "cairo_surface_write_to_png_stream")
		purego.RegisterLibFunc(&cairoSurfaceDestroy, cairo, "cairo_surface_destroy")
		purego.RegisterLibFunc(&cairoStatusToString, cairo, "cairo_status_to_string")
		purego.RegisterLibFunc(&gErrorFree, glib, "g_error_free")

		pngWriteCB = purego.NewCallback(func(closure, data, length uintptr) uintptr {
			defer recoverCB("png-write")
			captureMu.Lock()
			defer captureMu.Unlock()
			p := captures[closure]
			if p == nil {
				return cairoStatusWriteError
			}
			p.png = append(p.png, unsafe.Slice((*byte)(unsafe.Pointer(data)), uint32(length))...)
			return cairoStatusSuccess
		})
		snapshotDoneCB = purego.NewCallback(func(source, res, id uintptr) uintptr {
			defer recoverCB("snapshot-ready")
			finishCapture(source, res, id)
			return 0
		})
	})
	return captureInitErr
}

func finishCapture(view, res, id uintptr) {
	captureMu.Lock()
	p := captures[id]
	captureMu.Unlock()
	if p == nil {
		return
	}
	defer func() {
		captureMu.Lock()
		delete(captures, id)
		captureMu.Unlock()
	}()
	var gerr uintptr
	surface := wkGetSnapshotFinish(view, res, &gerr)
	if surface == 0 {
		msg := "no surface"
		if gerr != 0 {
			msg = goString(*(*uintptr)(unsafe.Pointer(gerr + 8)))
			gErrorFree(gerr)
		}
		p.ch <- captureResult{err: errors.New("webkitgtk: snapshot failed: " + msg)}
		return
	}
	defer cairoSurfaceDestroy(surface)
	if status := uint32(cairoWriteToPNG(surface, pngWriteCB, id)); status != uint32(cairoStatusSuccess) {
		p.ch <- captureResult{err: errors.New("webkitgtk: PNG encoding failed: " + cairoStatusToString(uintptr(status)))}
		return
	}
	captureMu.Lock()
	png := p.png
	captureMu.Unlock()
	p.ch <- captureResult{png: png}
}

func (v *view) Capture(ctx context.Context) ([]byte, error) {
	if err := initCapture(); err != nil {
		return nil, err
	}
	p := &pendingCapture{ch: make(chan captureResult, 1)}
	captureMu.Lock()
	captureSeq++
	id := captureSeq
	captures[id] = p
	captureMu.Unlock()
	dispatchMain(func() {
		wkGetSnapshot(v.wk, wkSnapshotRegionVisible, wkSnapshotOptionsNone, 0, snapshotDoneCB, id)
	})
	select {
	case r := <-p.ch:
		return r.png, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
