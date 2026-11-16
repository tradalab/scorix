package wkwebview

import (
	"testing"

	"github.com/tradalab/scorix/window"
)

func TestCtrlInAnAcceleratorIsCommandOnTheMacAndCtrlWithSuperKeepsBoth(t *testing.T) {
	cases := []struct {
		accel string
		key   string
		mods  uint64
	}{
		{"Ctrl+C", "c", nsModCommand},
		{"CmdOrCtrl+Q", "q", nsModCommand},
		{"Cmd+W", "w", nsModCommand},
		{"Ctrl+Shift+Z", "Z", nsModCommand},
		{"Ctrl+Shift+1", "1", nsModCommand | nsModShift},
		{"Shift+F5", string(rune(0xF708)), nsModShift},
		{"Alt+Ctrl+H", "h", nsModCommand | nsModOption},
		{"Ctrl+Cmd+F", "f", nsModCommand | nsModControl},
		{"F5", string(rune(0xF708)), 0},
		{"Ctrl+F12", string(rune(0xF70F)), nsModCommand},
		{"Ctrl+1", "1", nsModCommand},
		{"Delete", string(rune(0xF728)), 0},
		{"Ctrl+Enter", "\r", nsModCommand},
	}
	for _, c := range cases {
		a, err := window.ParseAccel(c.accel)
		if err != nil {
			t.Fatalf("%s: %v", c.accel, err)
		}
		key, mods := keyEquivalent(a)
		if key != c.key || mods != c.mods {
			t.Errorf("%s = (%q, %#x), want (%q, %#x)", c.accel, key, mods, c.key, c.mods)
		}
	}
	if key, mods := keyEquivalent(window.Accel{}); key != "" || mods != 0 {
		t.Errorf("empty accel = (%q, %#x), want nothing", key, mods)
	}
}

func TestEveryEditCommandHasAnAppKitSelector(t *testing.T) {
	for _, c := range []window.EditCommand{window.EditUndo, window.EditRedo, window.EditCut, window.EditCopy, window.EditPaste, window.EditSelectAll} {
		if editSelectors[c] == "" {
			t.Errorf("%s has no selector", c)
		}
	}
}

func TestTheDefaultEditMenuIsAddedOnlyWhenTheAppMenuCarriesNoEditCommand(t *testing.T) {
	without := []window.MenuItem{{Label: "File", Submenu: []window.MenuItem{{Label: "Open"}}}}
	if hasEditCommand(without) {
		t.Error("a menu with no edit command reported one")
	}
	nested := []window.MenuItem{{Label: "File"}, {Label: "Edit", Submenu: []window.MenuItem{{Label: "More", Submenu: []window.MenuItem{{Label: "Copy", Edit: window.EditCopy}}}}}}
	if !hasEditCommand(nested) {
		t.Error("an edit command two submenus deep was missed")
	}
}

func TestAPopupPointGoesFromTopLeftPageCoordinatesToBottomLeftScreenCoordinates(t *testing.T) {
	x, y := popupScreenPoint(100, 200, 0, 0, 600, 10, 50)
	if x != 110 || y != 750 {
		t.Fatalf("popup point = (%v, %v), want (110, 750)", x, y)
	}
}
