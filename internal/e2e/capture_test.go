package e2e

import (
	"bytes"
	"context"
	"image/png"
	"testing"
	"time"
)

func TestCaptureShowsWhatThePageDraws(t *testing.T) {
	a := harness(t)
	ask(t, `(document.body.style.background = "rgb(255, 0, 0)", true)`)
	t.Cleanup(func() { ask(t, `(document.body.style.background = "", true)`) })
	viewport := askInt(t, "window.innerWidth")

	deadline := time.Now().Add(10 * time.Second)
	var why string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		b, err := a.MainWindow().Capture(ctx)
		cancel()
		if err != nil {
			why = err.Error()
		} else if img, err := png.Decode(bytes.NewReader(b)); err != nil {
			why = "not a PNG: " + err.Error()
		} else {
			bounds := img.Bounds()
			r, g, bl, _ := img.At(bounds.Min.X+bounds.Dx()/2, bounds.Min.Y+bounds.Dy()/2).RGBA()
			switch {
			case bounds.Dx() < viewport:
				t.Fatalf("capture is %dpx wide for a %dpx viewport", bounds.Dx(), viewport)
			case r>>8 == 255 && g>>8 == 0 && bl>>8 == 0:
				t.Logf("capture %dx%d for a %dpx viewport", bounds.Dx(), bounds.Dy(), viewport)
				return
			default:
				why = "centre pixel is not the page's red"
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no capture of the red page in 10s: %s", why)
}
