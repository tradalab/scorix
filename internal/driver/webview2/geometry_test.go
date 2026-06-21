package webview2

import "testing"

func TestCenterPosition(t *testing.T) {
	if x, y := centerPosition(800, 600, 1920, 1080); x != 560 || y != 240 {
		t.Fatalf("center = %d,%d, want 560,240", x, y)
	}
	if x, y := centerPosition(2000, 1200, 1920, 1080); x != 0 || y != 0 {
		t.Fatalf("oversize center = %d,%d, want 0,0", x, y)
	}
}

func TestDPIScaling(t *testing.T) {
	// 100% (96 dpi) is identity; 150% (144) and 200% (192) scale round-trip.
	for _, tc := range []struct{ logical, dpi, phys int }{
		{1024, 96, 1024},
		{1024, 144, 1536},
		{1024, 192, 2048},
		{800, 120, 1000}, // 125%
		{0, 0, 0},        // dpi 0 treated as 96 (1:1)
	} {
		if got := toPhysical(tc.logical, tc.dpi); got != tc.phys {
			t.Errorf("toPhysical(%d, %d) = %d, want %d", tc.logical, tc.dpi, got, tc.phys)
		}
		if got := toLogical(tc.phys, tc.dpi); got != tc.logical {
			t.Errorf("toLogical(%d, %d) = %d, want %d", tc.phys, tc.dpi, got, tc.logical)
		}
	}
}

func TestClampSize(t *testing.T) {
	if w, h := clampSize(100, 100, 200, 200, 0, 0); w != 200 || h != 200 {
		t.Fatalf("min clamp = %d,%d, want 200,200", w, h)
	}
	if w, h := clampSize(5000, 5000, 0, 0, 1000, 800); w != 1000 || h != 800 {
		t.Fatalf("max clamp = %d,%d, want 1000,800", w, h)
	}
	if w, h := clampSize(640, 480, 0, 0, 0, 0); w != 640 || h != 480 {
		t.Fatalf("no-bound = %d,%d, want 640,480", w, h)
	}
}
