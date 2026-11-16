//go:build darwin && !ios

package wkwebview

import (
	"context"
	"errors"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/internal/mac"
)

const (
	nsBitmapImageFileTypePNG uint64 = 4
	snapshotGiveUp                  = 30 * time.Second
)

type snapshotResult struct {
	png []byte
	err error
}

var (
	snapshotMu      sync.Mutex
	snapshotChMu    sync.Mutex
	snapshotCh      chan snapshotResult
	snapshotOnce    sync.Once
	snapshotBlock   uintptr
	snapshotInitErr error
)

func snapshotHandler() (uintptr, error) {
	snapshotOnce.Do(func() {
		snapshotBlock, snapshotInitErr = mac.NewBlock(func(_ uintptr, image, nsErr objc.ID) {
			defer recoverCB("takeSnapshot completion")
			r := snapshotResult{}
			switch {
			case nsErr != 0:
				r.err = errors.New("wkwebview: snapshot failed: " + nsStringToGo(nsErr.Send(sel("localizedDescription"))))
			case image == 0:
				r.err = errors.New("wkwebview: snapshot returned no image")
			default:
				r.png, r.err = imagePNG(image)
			}
			snapshotChMu.Lock()
			ch := snapshotCh
			snapshotCh = nil
			snapshotChMu.Unlock()
			if ch != nil {
				ch <- r
			}
		})
	})
	return snapshotBlock, snapshotInitErr
}

func imagePNG(image objc.ID) ([]byte, error) {
	tiff := image.Send(sel("TIFFRepresentation"))
	if tiff == 0 {
		return nil, errors.New("wkwebview: snapshot has no bitmap")
	}
	rep := objc.ID(cls("NSBitmapImageRep")).Send(sel("imageRepWithData:"), tiff)
	if rep == 0 {
		return nil, errors.New("wkwebview: snapshot bitmap is unreadable")
	}
	props := objc.ID(cls("NSDictionary")).Send(sel("dictionary"))
	data := rep.Send(sel("representationUsingType:properties:"), nsBitmapImageFileTypePNG, props)
	if data == 0 {
		return nil, errors.New("wkwebview: PNG encoding failed")
	}
	n := objc.Send[uint64](data, sel("length"))
	p := objc.Send[uintptr](data, sel("bytes"))
	if n == 0 || p == 0 {
		return nil, errors.New("wkwebview: PNG encoding came back empty")
	}
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(p)), n)...), nil
}

func (v *view) Capture(ctx context.Context) ([]byte, error) {
	block, err := snapshotHandler()
	if err != nil {
		return nil, err
	}
	snapshotMu.Lock()
	ch := make(chan snapshotResult, 1)
	snapshotChMu.Lock()
	snapshotCh = ch
	snapshotChMu.Unlock()
	out := make(chan snapshotResult, 1)
	go func() {
		defer snapshotMu.Unlock()
		select {
		case r := <-ch:
			out <- r
		case <-time.After(snapshotGiveUp):
			snapshotChMu.Lock()
			if snapshotCh == ch {
				snapshotCh = nil
			}
			snapshotChMu.Unlock()
			out <- snapshotResult{err: errors.New("wkwebview: snapshot never completed")}
		}
	}()
	dispatchMain(func() {
		if !v.open() {
			snapshotChMu.Lock()
			if snapshotCh == ch {
				snapshotCh = nil
			}
			snapshotChMu.Unlock()
			ch <- snapshotResult{err: errors.New("wkwebview: the window is closed")}
			return
		}
		v.wk.Send(sel("takeSnapshotWithConfiguration:completionHandler:"), objc.ID(0), objc.ID(block))
	})
	select {
	case r := <-out:
		return r.png, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (v *view) open() bool {
	_, ok := viewByWK.Load(v.wk)
	return ok
}
