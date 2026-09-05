package window

import "testing"

func TestParseAccel(t *testing.T) {
	cases := []struct {
		in   string
		str  string
		mods uint32
		vk   uint32
		ok   bool
	}{
		{"Ctrl+Shift+K", "Ctrl+Shift+K", 0x2 | 0x4, 'K', true},
		{"alt+f5", "Alt+F5", 0x1, 0x74, true},
		{"CmdOrCtrl+Q", "Ctrl+Q", 0x2, 'Q', true},
		{"Win+Space", "Super+Space", 0x8, 0x20, true},
		{"F11", "F11", 0, 0x7A, true},
		{"Ctrl+PageUp", "Ctrl+PgUp", 0x2, 0x21, true},
		{"Ctrl+F24", "Ctrl+F24", 0x2, 0x70 + 23, true},
		{"Delete", "Del", 0, 0x2E, true}, // bare keys parse; the menu and hotkey layers decide what to allow
		{"Ctrl+Shift+Z", "Ctrl+Shift+Z", 0x2 | 0x4, 'Z', true},
		{"Ctrl+F25", "", 0, 0, false},
		{"Bogus+K", "", 0, 0, false},
		{"Ctrl+Widget", "", 0, 0, false},
		{"Ctrl+", "", 0, 0, false},
	}
	for _, c := range cases {
		a, err := ParseAccel(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("%q: err = %v", c.in, err)
		}
		if !c.ok {
			continue
		}
		mods, vk := a.Win32()
		if a.String() != c.str || mods != c.mods || vk != c.vk {
			t.Fatalf("%q: str=%q mods=%#x vk=%#x, want %q %#x %#x", c.in, a.String(), mods, vk, c.str, c.mods, c.vk)
		}
	}
}
