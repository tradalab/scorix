//go:build windows

package clipboard

import (
	"image"
	"image/color"
	"testing"
)

func TestImageClipboardRoundTrip(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	colors := []color.RGBA{
		{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255},
		{10, 20, 30, 255}, {200, 100, 50, 255}, {0, 0, 0, 255},
	}
	i := 0
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.SetRGBA(x, y, colors[i])
			i++
		}
	}

	if err := writeImage(src); err != nil {
		t.Fatalf("writeImage: %v", err)
	}
	got, err := readImage()
	if err != nil {
		t.Fatalf("readImage: %v", err)
	}
	if got.Bounds().Dx() != 3 || got.Bounds().Dy() != 2 {
		t.Fatalf("bounds = %v", got.Bounds())
	}
	i = 0
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			r, g, b, a := got.At(x, y).RGBA()
			want := colors[i]
			if byte(r>>8) != want.R || byte(g>>8) != want.G || byte(b>>8) != want.B || byte(a>>8) != want.A {
				t.Fatalf("pixel (%d,%d) = %d,%d,%d,%d want %v", x, y, r>>8, g>>8, b>>8, a>>8, want)
			}
			i++
		}
	}
}
