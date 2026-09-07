//go:build darwin

package wkwebview

import (
	"github.com/ebitengine/purego/objc"
	"github.com/tradalab/scorix/window"
)

// Both are probed with a type assertion at run time, so a renamed or re-signed
// method would silently stop being found instead of failing to build.
var (
	_ window.ScreenLister       = (*rt)(nil)
	_ window.FullscreenReporter = (*win)(nil)
)

// Without this the framework cannot reject a restored position that no longer
// lands on a monitor, and a window saved on an unplugged screen comes back
// invisible.
func (r *rt) Screens() []window.Screen { return onMainVal(enumerateScreens) }

// Split from the method so a test can reach it without a run loop to drain the
// main queue.
func enumerateScreens() []window.Screen {
	list := objc.ID(cls("NSScreen")).Send(sel("screens"))
	if list == 0 {
		return nil
	}
	n := objc.Send[uint64](list, sel("count"))
	if n == 0 {
		return nil
	}
	primary, ok := rectOf(objc.Send[objc.ID](list, sel("objectAtIndex:"), uint64(0)), "frame")
	if !ok {
		return nil
	}
	out := make([]window.Screen, 0, n)
	for i := uint64(0); i < n; i++ {
		s := objc.Send[objc.ID](list, sel("objectAtIndex:"), i)
		f, ok := rectOf(s, "frame")
		if !ok {
			continue
		}
		out = append(out, window.Screen{
			X:       int(f.Origin.X),
			Y:       int(flipY(primary.Size.H, f.Origin.Y, f.Size.H)),
			W:       int(f.Size.W),
			H:       int(f.Size.H),
			Primary: i == 0, // screens[0] is the one owning the menu bar
			Scale:   scaleOf(s),
		})
	}
	return out
}

// A Retina display reports 2.0. Anything unreadable is reported as 1.0 rather
// than 0, which would make every caller divide by zero.
func scaleOf(s objc.ID) float64 {
	if v := objc.Send[float64](s, sel("backingScaleFactor")); v > 0 {
		return v
	}
	return 1
}
