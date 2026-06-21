// Package webview2 is the Windows native window.Driver backed by WebView2.
package webview2

// centerPosition clamps a negative (oversize-window) origin to 0.
func centerPosition(w, h, screenW, screenH int) (int, int) {
	x := (screenW - w) / 2
	y := (screenH - h) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// defaultDPI is Windows' baseline (100% scale). The window API speaks logical
// (DIP) units measured at this DPI; physical pixels = logical * actualDPI / 96.
const defaultDPI = 96

// toPhysical converts a logical (DIP) measurement to physical pixels at dpi,
// rounded to nearest. dpi <= 0 is treated as defaultDPI (1:1).
func toPhysical(v, dpi int) int {
	if dpi <= 0 {
		dpi = defaultDPI
	}
	return (v*dpi + defaultDPI/2) / defaultDPI
}

// toLogical is the inverse of toPhysical: physical pixels at dpi back to DIPs.
func toLogical(v, dpi int) int {
	if dpi <= 0 {
		dpi = defaultDPI
	}
	return (v*defaultDPI + dpi/2) / dpi
}

// clampSize ignores any bound that is <= 0 (treated as unset).
func clampSize(w, h, minW, minH, maxW, maxH int) (int, int) {
	if minW > 0 && w < minW {
		w = minW
	}
	if minH > 0 && h < minH {
		h = minH
	}
	if maxW > 0 && w > maxW {
		w = maxW
	}
	if maxH > 0 && h > maxH {
		h = maxH
	}
	return w, h
}
