package window

import (
	"fmt"
	"strconv"
	"strings"
)

// Accel is a keyboard accelerator in portable form: Key is an upper-case
// letter or digit, "F1".."F24", or a name from accelNames. Drivers map it to
// their own key codes.
type Accel struct {
	Ctrl  bool
	Shift bool
	Alt   bool
	Super bool
	Key   string
}

var accelNames = map[string]uint32{ // name -> Win32 virtual key
	"SPACE": 0x20, "TAB": 0x09, "ENTER": 0x0D, "RETURN": 0x0D,
	"ESC": 0x1B, "ESCAPE": 0x1B, "BACKSPACE": 0x08, "DELETE": 0x2E,
	"HOME": 0x24, "END": 0x23, "PAGEUP": 0x21, "PAGEDOWN": 0x22,
	"UP": 0x26, "DOWN": 0x28, "LEFT": 0x25, "RIGHT": 0x27,
}

var accelDisplay = map[string]string{
	"SPACE": "Space", "TAB": "Tab", "ENTER": "Enter", "RETURN": "Enter",
	"ESC": "Esc", "ESCAPE": "Esc", "BACKSPACE": "Backspace", "DELETE": "Del",
	"HOME": "Home", "END": "End", "PAGEUP": "PgUp", "PAGEDOWN": "PgDn",
	"UP": "Up", "DOWN": "Down", "LEFT": "Left", "RIGHT": "Right",
}

// ParseAccel reads "Ctrl+Shift+K", "alt+f5", "CmdOrCtrl+Q". A bare key is
// accepted here; callers that bind system-wide must reject it themselves.
func ParseAccel(s string) (Accel, error) {
	var a Accel
	parts := strings.Split(s, "+")
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl", "control", "cmdorctrl", "commandorcontrol": // CmdOrCtrl is Ctrl until the mac driver grows a menu
			a.Ctrl = true
		case "alt", "option":
			a.Alt = true
		case "shift":
			a.Shift = true
		case "win", "super", "cmd", "meta", "command":
			a.Super = true
		default:
			return Accel{}, fmt.Errorf("unknown modifier %q in %q", p, s)
		}
	}
	key := strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	switch {
	case key == "":
		return Accel{}, fmt.Errorf("empty key in %q", s)
	case len(key) == 1 && (key[0] >= 'A' && key[0] <= 'Z' || key[0] >= '0' && key[0] <= '9'):
	case isFKey(key):
		n, err := strconv.Atoi(key[1:])
		if err != nil || n < 1 || n > 24 {
			return Accel{}, fmt.Errorf("bad key %q in %q", key, s)
		}
	default:
		if _, ok := accelNames[key]; !ok {
			return Accel{}, fmt.Errorf("unknown key %q in %q", key, s)
		}
	}
	a.Key = key
	return a, nil
}

func isFKey(key string) bool {
	return len(key) >= 2 && len(key) <= 3 && key[0] == 'F' && key[1] >= '0' && key[1] <= '9'
}

func (a Accel) IsZero() bool { return a.Key == "" }

// IsFunctionKey reports F1..F24. Callers gate bare accelerators on it, so it
// must not be spelled as a "F" prefix test: the letter F is a typing key.
func (a Accel) IsFunctionKey() bool { return isFKey(a.Key) }

// String is the canonical display form, e.g. "Ctrl+Shift+K".
func (a Accel) String() string {
	var parts []string
	if a.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if a.Shift {
		parts = append(parts, "Shift")
	}
	if a.Alt {
		parts = append(parts, "Alt")
	}
	if a.Super {
		parts = append(parts, "Super")
	}
	key := a.Key
	if d, ok := accelDisplay[key]; ok {
		key = d
	}
	return strings.Join(append(parts, key), "+")
}

// Win32 returns the MOD_* mask and virtual-key code that RegisterHotKey and
// ACCEL tables take.
func (a Accel) Win32() (mods, vk uint32) {
	if a.Alt {
		mods |= 0x1
	}
	if a.Ctrl {
		mods |= 0x2
	}
	if a.Shift {
		mods |= 0x4
	}
	if a.Super {
		mods |= 0x8
	}
	switch {
	case len(a.Key) == 1:
		vk = uint32(a.Key[0])
	case isFKey(a.Key):
		n, _ := strconv.Atoi(a.Key[1:])
		vk = uint32(0x70 + n - 1) // VK_F1
	default:
		vk = accelNames[a.Key]
	}
	return mods, vk
}
