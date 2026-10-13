package systemtray

import (
	"bytes"
	"encoding/binary"
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// The Linux tray decodes PNG only, and apps embed the .ico the Windows tray
// needs. `scorix icon` stores every ICO frame as PNG, so the largest one is
// handed over as is; anything else is returned unchanged.
func pngIcon(b []byte) []byte {
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return b
	}
	var best []byte
	bestW := 0
	for i := range int(binary.LittleEndian.Uint16(b[4:])) {
		e := 6 + 16*i
		if e+16 > len(b) {
			break
		}
		w := int(b[e])
		if w == 0 {
			w = 256
		}
		// uint64: on a 32-bit build int(off)+int(size) can wrap past the check.
		size := uint64(binary.LittleEndian.Uint32(b[e+8:]))
		off := uint64(binary.LittleEndian.Uint32(b[e+12:]))
		if off+size > uint64(len(b)) || !bytes.HasPrefix(b[off:off+size], pngMagic) {
			continue
		}
		if w > bestW {
			best, bestW = b[off:off+size], w
		}
	}
	if best == nil {
		return b
	}
	return best
}
