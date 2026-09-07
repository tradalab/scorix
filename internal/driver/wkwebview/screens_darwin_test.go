//go:build darwin

package wkwebview

import "testing"

// Reaches enumerateScreens directly: going through Screens would need a run loop
// to drain the main queue, which a test binary does not have.
func TestEnumerateScreens(t *testing.T) {
	if err := initObjC(); err != nil {
		t.Fatalf("initObjC: %v", err)
	}
	screens := enumerateScreens()
	if len(screens) == 0 {
		t.Skip("no screen attached to this runner")
	}
	if !screens[0].Primary {
		t.Error("screens[0] must be the primary: it is where the global origin is")
	}
	for i, s := range screens {
		if s.W <= 0 || s.H <= 0 {
			t.Errorf("screen %d has no size: %+v", i, s)
		}
		if s.Scale <= 0 {
			t.Errorf("screen %d scale = %v, callers divide by this", i, s.Scale)
		}
	}
	// The primary defines the origin, so its top edge is y=0 in the flipped space.
	if screens[0].Y != 0 {
		t.Errorf("primary screen Y = %d, want 0", screens[0].Y)
	}
	t.Logf("screens: %+v", screens)
}
