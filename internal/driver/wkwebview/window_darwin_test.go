//go:build darwin

package wkwebview

import (
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// nsRect has to match CGRect field for field, and getValue:size: has to copy
// through the pointer. Neither is checked by the compiler, and both are what
// Size and Position stand on.
func TestRectRoundTripsThroughNSValue(t *testing.T) {
	if err := initObjC(); err != nil {
		t.Fatalf("initObjC: %v", err)
	}
	if got := unsafe.Sizeof(nsRect{}); got != 32 {
		t.Fatalf("sizeof(nsRect) = %d, want 32", got)
	}

	// Bound here rather than in the driver: only this test writes an NSValue.
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		t.Fatalf("dlopen libobjc: %v", err)
	}
	var valueWithBytes func(objc.ID, objc.SEL, unsafe.Pointer, string) objc.ID
	purego.RegisterLibFunc(&valueWithBytes, lib, "objc_msgSend")

	in := nsRect{Origin: nsPoint{X: 12, Y: 34}, Size: nsSize{W: 567, H: 89}}
	v := valueWithBytes(objc.ID(cls("NSValue")), sel("valueWithBytes:objCType:"),
		unsafe.Pointer(&in), "{CGRect={CGPoint=dd}{CGSize=dd}}")
	if v == 0 {
		t.Fatal("valueWithBytes:objCType: returned nil")
	}
	var out nsRect
	msgSendGetValue(v, sel("getValue:size:"), unsafe.Pointer(&out), uint64(unsafe.Sizeof(out)))
	if out != in {
		t.Fatalf("rect round trip = %+v, want %+v", out, in)
	}
}

// Exercises rectOf against a real AppKit object, which is the half the NSValue
// test cannot reach: KVC has to box the property before getValue: can read it.
func TestRectOfReadsARealFrame(t *testing.T) {
	if err := initObjC(); err != nil {
		t.Fatalf("initObjC: %v", err)
	}
	h, ok := primaryScreenHeight()
	if !ok {
		t.Skip("no screen attached to this runner")
	}
	if h <= 0 {
		t.Fatalf("primary screen height = %v, want a positive height", h)
	}
	t.Logf("primary screen height = %v", h)
}

// A test binary runs no NSApplication, so nothing drains the main queue. onMain
// has to give up rather than block its caller forever - Size, Position and State
// all sit on it, and one of them is called while the app is quitting.
func TestOnMainGivesUpWithoutARunLoop(t *testing.T) {
	if err := initObjC(); err != nil {
		t.Fatalf("initObjC: %v", err)
	}
	done := make(chan struct{})
	go func() { onMain(func() {}); close(done) }()
	select {
	case <-done:
	case <-time.After(mainQueueWait + 5*time.Second):
		t.Fatal("onMain blocked forever with no run loop")
	}
}

// SetPosition and Position must round-trip, which holds only because the
// conversion is its own inverse.
func TestFlipYIsItsOwnInverse(t *testing.T) {
	const screenH, winH = 1080, 700
	for _, top := range []float64{0, 50, 380, 1080 - winH} {
		origin := flipY(screenH, top, winH)
		if back := flipY(screenH, origin, winH); back != top {
			t.Errorf("flipY round trip: top %v -> origin %v -> %v", top, origin, back)
		}
	}
	// A window sitting on the bottom edge is screenH-winH from the top.
	if got := flipY(screenH, 0, winH); got != screenH-winH {
		t.Errorf("a window at the top has origin %v, want %v", got, screenH-winH)
	}
}

// Resizing must not walk the window down the screen: the top edge is what stays
// put on every other platform (SWP_NOMOVE keeps the top-left).
func TestPinTopKeepsTheTopEdge(t *testing.T) {
	const originY, oldH = 100.0, 600.0
	top := originY + oldH
	for _, newH := range []float64{600, 700, 400, 1} {
		got := pinTop(originY, oldH, newH)
		if got+newH != top {
			t.Errorf("height %v -> origin %v, top edge %v, want %v", newH, got, got+newH, top)
		}
	}
	if got := pinTop(originY, oldH, oldH); got != originY {
		t.Errorf("an unchanged height moved the window: %v, want %v", got, originY)
	}
}
