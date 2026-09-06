//go:build darwin

package mac

import (
	"strings"
	"testing"

	"github.com/ebitengine/purego/objc"
)

// These run on the CI macos runner, which is the only place the Objective-C ABI
// can answer for itself. They deliberately avoid the window server: object
// creation and message sends only, no status bar and no notification centre.
func mustInit(t *testing.T) {
	t.Helper()
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

// Proves every class the modules reach for actually resolves - including the
// UserNotifications ones, which is how a wrong framework path would show up.
func TestClassesResolve(t *testing.T) {
	mustInit(t)
	for _, name := range []string{
		"NSString", "NSData", "NSBundle", "NSNumber", "NSImage",
		"NSMenu", "NSMenuItem", "NSStatusBar", "NSMutableArray", "NSMutableSet",
		"UNUserNotificationCenter", "UNMutableNotificationContent",
		"UNNotificationAction", "UNNotificationCategory",
		"UNNotificationRequest", "UNNotificationSound",
	} {
		if Class(name) == 0 {
			t.Errorf("class %s did not resolve", name)
		}
	}
}

// The string binding carries a Go string into C, which is the one purego
// conversion every other call depends on.
func TestNSStringRoundTrip(t *testing.T) {
	mustInit(t)
	for _, s := range []string{"", "hello", "tiếng Việt", "emoji 🎉", strings.Repeat("x", 4096)} {
		withPool(func() {
			if got := GoString(NSString(s)); got != s {
				t.Errorf("round trip = %q, want %q", got, s)
			}
		})
	}
}

func TestNSData(t *testing.T) {
	mustInit(t)
	withPool(func() {
		b := []byte{0, 1, 2, 250, 255}
		d := NSData(b)
		if d == 0 {
			t.Fatal("NSData returned nil for a non-empty slice")
		}
		if n := objc.Send[uint64](d, Sel("length")); n != uint64(len(b)) {
			t.Fatalf("length = %d, want %d", n, len(b))
		}
	})
	if NSData(nil) != 0 {
		t.Fatal("empty input must not produce an NSData")
	}
}

// A CGFloat argument goes in a floating-point register, not an integer one, so
// the wrong binding silently passes garbage. Read back as text to keep the
// assertion off the float RETURN convention, which is a separate question.
func TestFloatArgument(t *testing.T) {
	mustInit(t)
	for _, tc := range []struct {
		in   float64
		want string
	}{{-1, "-1"}, {18.5, "18.5"}, {0, "0"}} {
		withPool(func() {
			n := SendFloat(Class("NSNumber"), Sel("numberWithDouble:"), tc.in)
			if n == 0 {
				t.Fatalf("numberWithDouble:%v returned nil", tc.in)
			}
			if got := GoString(n.Send(Sel("stringValue"))); got != tc.want {
				t.Errorf("numberWithDouble:%v -> %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// setSize: passes a two-double struct by value. A wrong struct convention
// usually corrupts the argument registers, so the object is asked to describe
// itself afterwards: the description is logged rather than matched, because its
// exact text is not contractual.
func TestSizeArgumentDoesNotCorrupt(t *testing.T) {
	mustInit(t)
	withPool(func() {
		img := Class("NSImage").Send(Sel("alloc")).Send(Sel("init"))
		if img == 0 {
			t.Fatal("NSImage init returned nil")
		}
		defer img.Send(Sel("release"))
		SendSize(img, Sel("setSize:"), Size{W: 18, H: 24})
		desc := GoString(img.Send(Sel("description")))
		if desc == "" {
			t.Fatal("the object stopped answering after setSize:")
		}
		t.Logf("NSImage after setSize:{18,24} -> %s", desc)
	})
}

// The guard that keeps UNUserNotificationCenter from raising: a go test binary
// is not inside a .app, so this must come back empty.
func TestBundleIDIsEmptyOutsideABundle(t *testing.T) {
	mustInit(t)
	if id := BundleID(); id != "" {
		t.Fatalf("BundleID = %q, want empty for a bare test binary", id)
	}
}

// Reachable from IPC before OnStart has run Init, so it must not panic there.
func TestDispatchMainSurvivesWithoutARunLoop(t *testing.T) {
	mustInit(t)
	DispatchMain(func() { t.Error("no run loop in a test binary, this must not run") })
}
