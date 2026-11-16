package wkwebview

import (
	"strconv"
	"strings"

	"github.com/tradalab/scorix/window"
)

const (
	nsModShift   uint64 = 1 << 17
	nsModControl uint64 = 1 << 18
	nsModOption  uint64 = 1 << 19
	nsModCommand uint64 = 1 << 20
)

var editSelectors = map[window.EditCommand]string{
	window.EditUndo:      "undo:",
	window.EditRedo:      "redo:",
	window.EditCut:       "cut:",
	window.EditCopy:      "copy:",
	window.EditPaste:     "paste:",
	window.EditSelectAll: "selectAll:",
}

var keyNames = map[string]rune{
	"SPACE": ' ', "TAB": '\t', "ENTER": '\r', "RETURN": '\r',
	"ESC": 0x1b, "ESCAPE": 0x1b, "BACKSPACE": 0x08, "DELETE": 0xF728,
	"HOME": 0xF729, "END": 0xF72B, "PAGEUP": 0xF72C, "PAGEDOWN": 0xF72D,
	"UP": 0xF700, "DOWN": 0xF701, "LEFT": 0xF702, "RIGHT": 0xF703,
}

func keyEquivalent(a window.Accel) (string, uint64) {
	if a.IsZero() {
		return "", 0
	}
	shiftedLetter := a.Shift && len(a.Key) == 1 && a.Key[0] >= 'A' && a.Key[0] <= 'Z'
	var key string
	switch {
	case shiftedLetter:
		key = a.Key
	case len(a.Key) == 1:
		key = strings.ToLower(a.Key)
	case a.IsFunctionKey():
		n, err := strconv.Atoi(a.Key[1:])
		if err != nil {
			return "", 0
		}
		key = string(rune(0xF704 + n - 1))
	default:
		r, ok := keyNames[a.Key]
		if !ok {
			return "", 0
		}
		key = string(r)
	}
	var mods uint64
	switch {
	case a.Super && a.Ctrl:
		mods |= nsModCommand | nsModControl
	case a.Super || a.Ctrl:
		mods |= nsModCommand
	}
	if a.Shift && !shiftedLetter {
		mods |= nsModShift
	}
	if a.Alt {
		mods |= nsModOption
	}
	return key, mods
}

func hasEditCommand(items []window.MenuItem) bool {
	for _, it := range items {
		if it.Edit != "" || hasEditCommand(it.Submenu) {
			return true
		}
	}
	return false
}

func popupScreenPoint(frameX, frameY, contentX, contentY, contentH float64, x, y int) (float64, float64) {
	return frameX + contentX + float64(x), frameY + contentY + contentH - float64(y)
}
