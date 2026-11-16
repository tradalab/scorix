//go:build windows

package webview2

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"
)

const (
	cwvCapturePreview       = 30
	capturePreviewFormatPNG = 0
	istreamSeek             = 5
	streamSeekEnd           = 2
	handlerReleaseGrace     = 5 * time.Second
)

var (
	procCreateStreamOnHGlobal = ole32.NewProc("CreateStreamOnHGlobal")
	procGetHGlobalFromStream  = ole32.NewProc("GetHGlobalFromStream")
	procGlobalSize            = kernel32.NewProc("GlobalSize")
	procGlobalLock            = kernel32.NewProc("GlobalLock")
	procGlobalUnlock          = kernel32.NewProc("GlobalUnlock")
)

type captureResult struct {
	png []byte
	err error
}

func (v *webview2View) Capture(ctx context.Context) ([]byte, error) {
	ch := make(chan captureResult, 1)
	v.dispatch(func() { v.startCapture(ch) })
	select {
	case r := <-ch:
		return r.png, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (v *webview2View) startCapture(ch chan<- captureResult) {
	v.mu.Lock()
	core := v.core
	v.mu.Unlock()
	if core == nil {
		ch <- captureResult{err: errors.New("webview2: the page is not up yet")}
		return
	}
	stream := new(unsafe.Pointer)
	if hr, _, _ := procCreateStreamOnHGlobal.Call(0, 1, uintptr(unsafe.Pointer(stream))); int32(hr) < 0 {
		ch <- captureResult{err: fmt.Errorf("webview2: CreateStreamOnHGlobal hr=0x%x", uint32(hr))}
		return
	}
	var h *handler
	h = newHandler(func(errorCode, _ unsafe.Pointer) {
		time.AfterFunc(handlerReleaseGrace, func() { handlerKeep.Delete(h) })
		defer comCall(*stream, iunknownRelease)
		if hr := int32(uintptr(errorCode)); hr < 0 {
			ch <- captureResult{err: fmt.Errorf("webview2: CapturePreview hr=0x%x", uint32(hr))}
			return
		}
		png, err := streamBytes(*stream)
		ch <- captureResult{png: png, err: err}
	})
	if hr := int32(comCall(core, cwvCapturePreview, capturePreviewFormatPNG, uintptr(*stream), uintptr(unsafe.Pointer(h)))); hr < 0 {
		handlerKeep.Delete(h)
		comCall(*stream, iunknownRelease)
		ch <- captureResult{err: fmt.Errorf("webview2: CapturePreview hr=0x%x", uint32(hr))}
	}
}

func streamBytes(stream unsafe.Pointer) ([]byte, error) {
	hg := new(uintptr)
	if hr, _, _ := procGetHGlobalFromStream.Call(uintptr(stream), uintptr(unsafe.Pointer(hg))); int32(hr) < 0 {
		return nil, fmt.Errorf("webview2: GetHGlobalFromStream hr=0x%x", uint32(hr))
	}
	size := new(uint64)
	if hr := int32(comCall(stream, istreamSeek, 0, streamSeekEnd, uintptr(unsafe.Pointer(size)))); hr < 0 {
		return nil, fmt.Errorf("webview2: IStream::Seek hr=0x%x", uint32(hr))
	}
	if held, _, _ := procGlobalSize.Call(*hg); uint64(held) < *size {
		return nil, fmt.Errorf("webview2: the capture says %d bytes but holds %d", *size, held)
	}
	p, _, _ := procGlobalLock.Call(*hg)
	if p == 0 || *size == 0 {
		return nil, errors.New("webview2: the capture came back empty")
	}
	defer procGlobalUnlock.Call(*hg)
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(p)), *size)...), nil
}
