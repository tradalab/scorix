// Package ico reads the icon files `scorix icon` writes: an ICO container whose
// frames are all PNG.
package ico

import (
	"bytes"
	"encoding/binary"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// gdk-pixbuf's ICO loader refuses PNG frames, which are all `scorix icon` writes,
// while a PNG decoder is present on every platform. ok is false for anything else.
func LargestPNG(data []byte) (frame []byte, ok bool) {
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:]) != 0 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		return nil, false
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	best := -1
	for i := range count {
		entry := 6 + 16*i
		if entry+16 > len(data) {
			return nil, false
		}
		width := int(data[entry])
		if width == 0 {
			width = 256 // one byte cannot say 256, so the format says 0
		}
		size := int(binary.LittleEndian.Uint32(data[entry+8:]))
		offset := int(binary.LittleEndian.Uint32(data[entry+12:]))
		if offset < 0 || size < len(pngSignature) || offset > len(data)-size {
			continue
		}
		body := data[offset : offset+size]
		if !bytes.HasPrefix(body, pngSignature) || width <= best {
			continue
		}
		best, frame = width, body
	}
	return frame, frame != nil
}
