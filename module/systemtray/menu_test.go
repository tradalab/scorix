package systemtray

import (
	"testing"

	"github.com/tradalab/scorix/menu"
)

type fakeApp struct{ shown, closed int }

func (f *fakeApp) Show()                  { f.shown++ }
func (f *fakeApp) Close()                 { f.closed++ }
func (f *fakeApp) OnOpenURL(func(string)) {}

func TestDefaultMenuResolvesToShowAndQuit(t *testing.T) {
	app := &fakeApp{}
	got := resolveTray(defaultMenu(), app, 0)
	if len(got) != 2 || got[0].label != "Open" || got[1].label != "Quit" {
		t.Fatalf("default tray = %+v", got)
	}
	if got[0].tooltip != "Open Application" || got[1].tooltip != "Quit Application" {
		t.Fatalf("tooltip lost, the 5 shipping apps would see a changed tray: %+v", got)
	}
	got[0].onClick()
	got[1].onClick()
	if app.shown != 1 || app.closed != 1 {
		t.Fatalf("roles did not reach the app: shown=%d closed=%d", app.shown, app.closed)
	}
}

func TestResolveTrayCarriesStateAndDropsWhatATrayCannotDo(t *testing.T) {
	clicked := 0
	items := menu.Menu{
		menu.Separator(),
		{Label: "Ping", Accelerator: "Ctrl+P", OnClick: func() { clicked++ }}, // accel ignored, click kept
		{Label: "Verbose", Checked: true, Disabled: true},
		menu.Sub("More",
			menu.Separator(), // systray has no separator inside a submenu
			menu.Item{Label: "Deep"}),
		{Role: menu.RoleCopy},   // a window role: labelled, but display-only
		{Role: "nonsense"},      // unknown role and no label
		{Accelerator: "Ctrl+K"}, // no label at all
	}
	got := resolveTray(items, &fakeApp{}, 0)

	if len(got) != 5 {
		t.Fatalf("want 5 entries (2 label-less dropped), got %d: %+v", len(got), got)
	}
	if !got[0].separator {
		t.Fatalf("top-level separator lost: %+v", got[0])
	}
	got[1].onClick()
	if clicked != 1 || got[1].label != "Ping" {
		t.Fatalf("click lost: %+v", got[1])
	}
	if !got[2].checked || !got[2].disabled {
		t.Fatalf("checked/disabled lost: %+v", got[2])
	}
	if len(got[3].children) != 1 || got[3].children[0].label != "Deep" {
		t.Fatalf("submenu separator should be dropped, one child left: %+v", got[3])
	}
	if got[4].label != "Copy" || got[4].onClick != nil {
		t.Fatalf("a window role must resolve its label but stay display-only: %+v", got[4])
	}
}

// The tray menu is Go-declared, so there is no sys:menu event to catch a click
// the way the window bar does. An id-only item is therefore dead, not deferred.
func TestResolveTrayLeavesIDOnlyItemWithoutAnAction(t *testing.T) {
	got := resolveTray(menu.Menu{
		{ID: "settings", Label: "Settings"},
		{Label: "Connected: 3"}, // deliberately display-only, must stay silent
	}, &fakeApp{}, 0)
	if len(got) != 2 {
		t.Fatalf("tray = %+v", got)
	}
	if got[0].onClick != nil || got[1].onClick != nil {
		t.Fatalf("nothing here should have gained an action: %+v", got)
	}
}

func TestResolveTraySurvivesNilApp(t *testing.T) {
	got := resolveTray(defaultMenu(), nil, 0) // web/server mode: no AppController
	if len(got) != 2 {
		t.Fatalf("tray = %+v", got)
	}
	for _, n := range got {
		if n.onClick != nil {
			t.Fatalf("role bound to a nil app: %+v", n)
		}
	}
}
