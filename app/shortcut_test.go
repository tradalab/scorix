package app

import "testing"

func TestParseAccelerator(t *testing.T) {
	cases := []struct {
		in   string
		mods uint32
		vk   uint32
		ok   bool
	}{
		{"Ctrl+Shift+K", modControl | modShift | modNoRepeat, 'K', true},
		{"alt+f5", modAlt | modNoRepeat, 0x74, true},
		{"Win+Space", modWin | modNoRepeat, 0x20, true},
		{"Ctrl+9", modControl | modNoRepeat, '9', true},
		{"Ctrl+F24", modControl | modNoRepeat, 0x70 + 23, true},
		{"CmdOrCtrl+K", modControl | modNoRepeat, 'K', true},
		{"K", 0, 0, false},        // bare key would swallow typing
		{"Ctrl+F25", 0, 0, false}, // out of range
		{"Bogus+K", 0, 0, false},
		{"Ctrl+Widget", 0, 0, false},
	}
	for _, c := range cases {
		mods, vk, err := parseAccelerator(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("%q: err = %v", c.in, err)
		}
		if c.ok && (mods != c.mods || vk != c.vk) {
			t.Fatalf("%q: mods=%#x vk=%#x, want %#x %#x", c.in, mods, vk, c.mods, c.vk)
		}
	}
}
