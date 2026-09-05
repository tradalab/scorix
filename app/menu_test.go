package app

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/menu"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

func newMenuApp(t *testing.T, m menu.Menu) (*App, func()) {
	t.Helper()
	withHeadlessDriver(t)
	a, err := New(Options{URL: "scorix://app/index.html", Menu: m})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	return a, runHeadless(t, a)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(what)
}

func waitReply(t *testing.T, w window.Window, id string) webview.Message {
	t.Helper()
	var got webview.Message
	waitUntil(t, "command "+id+" never answered", func() bool {
		for _, raw := range headless.Sent(w) {
			var m webview.Message
			if json.Unmarshal(raw, &m) == nil && m.ID == id && (m.State == "done" || m.State == "error") {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

func TestMenuRolesAndCallbacks(t *testing.T) {
	pinged := make(chan struct{}, 1)
	a, stop := newMenuApp(t, menu.Menu{
		menu.Sub("File",
			menu.Item{Label: "Ping", Accelerator: "Ctrl+Shift+P", OnClick: func() { pinged <- struct{}{} }},
			menu.Separator(),
			menu.Std(menu.RoleQuit),
			menu.Item{ID: "ghost"}),
		menu.Sub("Edit", menu.Std(menu.RoleCopy), menu.Item{Role: menu.RolePaste, Label: "Paste It"}),
		menu.Sub("Help", menu.Item{ID: "about", Label: "About Test"}),
		menu.Sub("View",
			menu.Sub("Zoom", menu.Item{Label: "In", Disabled: true, Checked: true, Accelerator: "Ctrl+Widget"}),
			menu.Item{Label: "Solo", Accelerator: "K"},
			menu.Item{Label: "Rescan", Accelerator: "F5"},
			menu.Item{Label: "Find", Accelerator: "F"},
			menu.Item{Label: "Erase", Accelerator: "Delete", AccelHint: true}),
	})
	defer stop()
	w := a.MainWindow().Window

	bar := headless.MenuOf(w)
	if len(bar) != 4 || bar[0].Label != "File" || len(bar[0].Submenu) != 3 { // the label-less item is dropped
		t.Fatalf("bar = %+v", bar)
	}
	zoomIn := bar[3].Submenu[0].Submenu[0]
	if !zoomIn.Disabled || !zoomIn.Checked || !zoomIn.Accel.IsZero() {
		t.Fatalf("nested item = %+v", zoomIn)
	}
	if !bar[3].Submenu[1].Accel.IsZero() || bar[3].Submenu[2].Accel.String() != "F5" {
		t.Fatalf("bare accelerators: %+v / %+v", bar[3].Submenu[1], bar[3].Submenu[2])
	}
	if !bar[3].Submenu[3].Accel.IsZero() { // "F" is a typing key, not a function key
		t.Fatalf("bare F bound: %+v", bar[3].Submenu[3])
	}
	if erase := bar[3].Submenu[4]; erase.Accel.String() != "Del" || !erase.AccelHint {
		t.Fatalf("AccelHint should show a bare key without binding it: %+v", erase)
	}
	ping := bar[0].Submenu[0]
	if ping.Accel.String() != "Ctrl+Shift+P" || ping.AccelHint {
		t.Fatalf("custom item = %+v", ping)
	}
	if !bar[0].Submenu[1].Separator {
		t.Fatal("separator lost")
	}
	quit := bar[0].Submenu[2]
	if quit.Label != "Quit" || quit.Accel.String() != "Ctrl+Q" || quit.AccelHint {
		t.Fatalf("quit role = %+v", quit)
	}
	cp := bar[1].Submenu[0]
	if cp.Label != "Copy" || cp.Accel.String() != "Ctrl+C" || !cp.AccelHint {
		t.Fatalf("copy role = %+v", cp)
	}
	if bar[1].Submenu[1].Label != "Paste It" || !bar[1].Submenu[1].AccelHint {
		t.Fatalf("label override lost: %+v", bar[1].Submenu[1])
	}

	if !headless.ClickMenu(w, "Ping") {
		t.Fatal("Ping not clickable")
	}
	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("OnClick never ran")
	}

	headless.ClickMenu(w, "Copy")
	waitUntil(t, "copy role never reached the driver", func() bool {
		e := headless.EditCommands(w)
		return len(e) == 1 && e[0] == window.EditCopy
	})

	headless.ClickMenu(w, "About Test")
	m := waitSentEvent(t, w, "sys:menu")
	var got struct{ ID, Label string }
	_ = json.Unmarshal(m.Data, &got)
	if got.ID != "about" || got.Label != "About Test" {
		t.Fatalf("sys:menu = %s", m.Data)
	}

	a.SetMenu(nil)
	waitUntil(t, "SetMenu(nil) did not clear the bar", func() bool { return len(headless.MenuOf(w)) == 0 })
}

func TestFullscreenRoleFollowsDriver(t *testing.T) {
	a, stop := newMenuApp(t, menu.Menu{menu.Sub("View", menu.Std(menu.RoleToggleFullscreen))})
	defer stop()
	w := a.MainWindow().Window

	w.SetFullscreen(true) // as if the window manager did it, behind the app's back
	if !headless.ClickMenu(w, "Toggle Full Screen") {
		t.Fatal("role item not clickable")
	}
	waitUntil(t, "the toggle re-entered fullscreen instead of leaving it", func() bool {
		return w.State() != window.StateFullscreen
	})
}

func TestWinPopupMenuCommand(t *testing.T) {
	a, stop := newMenuApp(t, nil)
	defer stop()
	w := a.MainWindow().Window
	if len(headless.MenuOf(w)) != 0 {
		t.Fatal("a menu bar appeared without one being configured")
	}

	raw, _ := json.Marshal(webview.Message{ID: "P1", Kind: "command", Name: "win:popupMenu", State: "start",
		Data: json.RawMessage(`{"items":[{"id":"del","label":"Delete"},{"separator":true},{"role":"copy"}]}`)})
	headless.Inject(w, raw)
	if r := waitReply(t, w, "P1"); r.State != "done" {
		t.Fatalf("popupMenu reply = %+v", r)
	}
	p := headless.Popups(w)
	if len(p) != 1 || len(p[0]) != 3 || p[0][2].Label != "Copy" {
		t.Fatalf("popups = %+v", p)
	}
	headless.ClickPopup(w, "Delete")
	m := waitSentEvent(t, w, "sys:menu")
	if !strings.Contains(string(m.Data), `"id":"del"`) {
		t.Fatalf("sys:menu = %s", m.Data)
	}
}

func TestWinSetMenuCommand(t *testing.T) {
	a, stop := newMenuApp(t, nil)
	defer stop()
	w := a.MainWindow().Window

	raw, _ := json.Marshal(webview.Message{ID: "M1", Kind: "command", Name: "win:setMenu", State: "start",
		Data: json.RawMessage(`{"items":[{"label":"Tools","submenu":[{"id":"scan","label":"Scan","accelerator":"F5"},` +
			`{"id":"rm","label":"Remove","accelerator":"Delete","accelHint":true}]}]}`)})
	headless.Inject(w, raw)
	if r := waitReply(t, w, "M1"); r.State != "done" {
		t.Fatalf("setMenu reply = %+v", r)
	}
	bar := headless.MenuOf(w)
	if len(bar) != 1 || bar[0].Label != "Tools" || len(bar[0].Submenu) != 2 || bar[0].Submenu[0].Accel.String() != "F5" {
		t.Fatalf("bar = %+v", bar)
	}
	if rm := bar[0].Submenu[1]; rm.Accel.String() != "Del" || !rm.AccelHint { // accelHint survives the json tag
		t.Fatalf("hinted accelerator lost over IPC: %+v", rm)
	}
	headless.ClickMenu(w, "Scan")
	if m := waitSentEvent(t, w, "sys:menu"); !strings.Contains(string(m.Data), `"id":"scan"`) {
		t.Fatalf("sys:menu = %s", m.Data)
	}
}
