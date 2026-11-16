//go:build linux && !android

package webkitgtk

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

func TestLiveCaptureReturnsWhatThePageDraws(t *testing.T) {
	if os.Getenv("SCORIX_GTK_LIVE") == "" {
		t.Skip("set SCORIX_GTK_LIVE=1 with a display")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	rt, err := New().NewRuntime(window.RuntimeConfig{NoWebviewSandbox: true})
	if err != nil {
		t.Skip(err)
	}
	type result struct {
		w, h       int
		r, g, b, a uint32
		err        error
	}
	done := make(chan result, 1)
	rt.On(window.RuntimeReady, func() {
		w, err := rt.Windows().New(window.Options{Title: "capture", Width: 320, Height: 200})
		if err != nil {
			done <- result{err: err}
			rt.Quit()
			return
		}
		w.View().LoadHTML(`<html><body style="margin:0;background:rgb(255,0,0)"></body></html>`)
		w.Show()
		go func() {
			defer rt.Quit()
			c := w.View().(webview.Capturer)
			deadline := time.Now().Add(10 * time.Second)
			var last result
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				b, err := c.Capture(ctx)
				cancel()
				if err != nil {
					last = result{err: err}
				} else if img, err := png.Decode(bytes.NewReader(b)); err != nil {
					last = result{err: err}
				} else {
					bounds := img.Bounds()
					r, g, bl, a := img.At(bounds.Dx()/2, bounds.Dy()/2).RGBA()
					last = result{w: bounds.Dx(), h: bounds.Dy(), r: r >> 8, g: g >> 8, b: bl >> 8, a: a >> 8}
					if last.r == 255 && last.g == 0 && last.b == 0 {
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			done <- last
		}()
	})
	if err := rt.Run(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.r != 255 || r.g != 0 || r.b != 0 {
		t.Fatalf("centre pixel = rgba(%d,%d,%d,%d) in a %dx%d capture, want the page's red", r.r, r.g, r.b, r.a, r.w, r.h)
	}
	if r.w < 300 || r.h < 150 {
		t.Fatalf("capture is %dx%d, want about the 320x200 window", r.w, r.h)
	}
	t.Logf("capture %dx%d", r.w, r.h)
}
