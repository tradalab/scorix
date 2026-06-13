// Package webview2 is the no-CGO Windows native window.Driver backed by WebView2.
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
