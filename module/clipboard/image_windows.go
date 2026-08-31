//go:build windows

package clipboard

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/fault"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard     = user32.NewProc("OpenClipboard")
	procCloseClipboard    = user32.NewProc("CloseClipboard")
	procEmptyClipboard    = user32.NewProc("EmptyClipboard")
	procSetClipboardData  = user32.NewProc("SetClipboardData")
	procGetClipboardData  = user32.NewProc("GetClipboardData")
	procIsFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGlobalAlloc       = kernel32.NewProc("GlobalAlloc")
	procGlobalLock        = kernel32.NewProc("GlobalLock")
	procGlobalUnlock      = kernel32.NewProc("GlobalUnlock")
	procGlobalFree        = kernel32.NewProc("GlobalFree")
	procGlobalSize        = kernel32.NewProc("GlobalSize")
)

const (
	cfDIB          = 8
	gmemMoveable   = 0x0002
	dibHeaderSize  = 40
	biRGB          = 0
	clipboardRetry = 5 // another process may hold the clipboard; retry briefly
)

func openClipboard() error {
	for i := 0; i < clipboardRetry; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fault.New(fault.CodeUnavailable, "clipboard is held by another process")
}

func writeImage(img image.Image) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return fault.New("invalid_request", "empty image")
	}

	var buf bytes.Buffer
	hdr := make([]byte, dibHeaderSize)
	binary.LittleEndian.PutUint32(hdr[0:], dibHeaderSize)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(w))
	binary.LittleEndian.PutUint32(hdr[8:], uint32(h))
	binary.LittleEndian.PutUint16(hdr[12:], 1)  // planes
	binary.LittleEndian.PutUint16(hdr[14:], 32) // bpp
	binary.LittleEndian.PutUint32(hdr[16:], biRGB)
	buf.Write(hdr)
	for y := b.Max.Y - 1; y >= b.Min.Y; y-- {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			buf.Write([]byte{byte(bb >> 8), byte(g >> 8), byte(r >> 8), byte(a >> 8)})
		}
	}
	data := buf.Bytes()

	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(hMem)
	if p == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("GlobalLock failed")
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(p)), len(data))
	copy(dst, data)
	procGlobalUnlock.Call(hMem)

	if r, _, _ := procSetClipboardData.Call(cfDIB, hMem); r == 0 {
		procGlobalFree.Call(hMem) // ownership only transfers on success
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func readImage() (image.Image, error) {
	if r, _, _ := procIsFormatAvailable.Call(cfDIB); r == 0 {
		return nil, fault.New(fault.CodeNotFound, "no image on the clipboard")
	}
	if err := openClipboard(); err != nil {
		return nil, err
	}
	defer procCloseClipboard.Call()

	hMem, _, _ := procGetClipboardData.Call(cfDIB)
	if hMem == 0 {
		return nil, fault.New(fault.CodeNotFound, "no image on the clipboard")
	}
	size, _, _ := procGlobalSize.Call(hMem)
	p, _, _ := procGlobalLock.Call(hMem)
	if p == 0 || size < dibHeaderSize {
		return nil, fmt.Errorf("clipboard DIB unreadable")
	}
	defer procGlobalUnlock.Call(hMem)
	data := make([]byte, size)
	copy(data, unsafe.Slice((*byte)(unsafe.Pointer(p)), size))

	hdrSize := binary.LittleEndian.Uint32(data[0:])
	w := int(int32(binary.LittleEndian.Uint32(data[4:])))
	h := int(int32(binary.LittleEndian.Uint32(data[8:])))
	bpp := int(binary.LittleEndian.Uint16(data[14:]))
	compression := binary.LittleEndian.Uint32(data[16:])
	if compression != biRGB || (bpp != 32 && bpp != 24) {
		return nil, fault.Errorf("invalid_request", "unsupported clipboard DIB (bpp=%d compression=%d)", bpp, compression)
	}
	bottomUp := h > 0
	if h < 0 {
		h = -h
	}
	stride := ((w*bpp + 31) / 32) * 4
	pixels := data[hdrSize:]
	if len(pixels) < stride*h {
		return nil, fmt.Errorf("clipboard DIB truncated")
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		srcY := y
		if bottomUp {
			srcY = h - 1 - y
		}
		row := pixels[srcY*stride:]
		for x := 0; x < w; x++ {
			var bb, g, r, a byte
			if bpp == 32 {
				bb, g, r, a = row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
				if a == 0 {
					a = 255
				}
			} else {
				bb, g, r, a = row[x*3], row[x*3+1], row[x*3+2], 255
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: bb, A: a})
		}
	}
	return img, nil
}
