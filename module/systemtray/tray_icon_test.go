package systemtray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, size, size))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The layout `scorix icon` writes: a directory, then one PNG per size.
func icoOf(frames map[int][]byte, order ...int) []byte {
	var dir, data bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&dir, le, [3]uint16{0, 1, uint16(len(order))})
	off := 6 + 16*len(order)
	for _, s := range order {
		f := frames[s]
		_ = binary.Write(&dir, le, [4]uint8{uint8(s % 256), uint8(s % 256), 0, 0})
		_ = binary.Write(&dir, le, [2]uint16{1, 32})
		_ = binary.Write(&dir, le, [2]uint32{uint32(len(f)), uint32(off)})
		data.Write(f)
		off += len(f)
	}
	return append(dir.Bytes(), data.Bytes()...)
}

func TestPNGIconTakesLargestFrame(t *testing.T) {
	frames := map[int][]byte{16: pngOf(t, 16), 256: pngOf(t, 256), 32: pngOf(t, 32)}
	got := pngIcon(icoOf(frames, 16, 256, 32))
	cfg, err := png.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 256 {
		t.Fatalf("width %d, want 256", cfg.Width)
	}
}

func TestPNGIconLeavesOtherInputAlone(t *testing.T) {
	p := pngOf(t, 16)
	if got := pngIcon(p); !bytes.Equal(got, p) {
		t.Fatal("a PNG was changed")
	}
	bmp := icoOf(map[int][]byte{32: []byte("BMP frame, not a PNG")}, 32)
	if got := pngIcon(bmp); !bytes.Equal(got, bmp) {
		t.Fatal("an ICO without a PNG frame was changed")
	}
	cut := icoOf(map[int][]byte{32: pngOf(t, 32)}, 32)[:30]
	if got := pngIcon(cut); !bytes.Equal(got, cut) {
		t.Fatal("a truncated ICO was changed")
	}
	far := icoOf(map[int][]byte{32: pngOf(t, 32)}, 32)
	binary.LittleEndian.PutUint32(far[6+8:], 0x7ffffff8)
	binary.LittleEndian.PutUint32(far[6+12:], 0x10)
	if got := pngIcon(far); !bytes.Equal(got, far) {
		t.Fatal("an ICO pointing past its end was changed")
	}
}
