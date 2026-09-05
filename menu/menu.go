// Package menu is the app-facing model for menu bars and context menus. Items
// are plain data so a frontend can send them over IPC; OnClick is Go-only.
package menu

import "runtime"

type Role string

const (
	RoleQuit             Role = "quit"
	RoleAbout            Role = "about"
	RoleUndo             Role = "undo"
	RoleRedo             Role = "redo"
	RoleCut              Role = "cut"
	RoleCopy             Role = "copy"
	RolePaste            Role = "paste"
	RoleSelectAll        Role = "selectAll"
	RoleMinimize         Role = "minimize"
	RoleClose            Role = "close"
	RoleToggleFullscreen Role = "toggleFullscreen"
	RoleDevTools         Role = "devTools"
	RoleReload           Role = "reload"
)

type Item struct {
	ID          string `json:"id,omitempty"` // carried by the sys:menu event when no Go handler claims the click
	Label       string `json:"label,omitempty"`
	Role        Role   `json:"role,omitempty"`
	Accelerator string `json:"accelerator,omitempty"` // "Ctrl+Shift+K"; a role supplies one when blank
	AccelHint   bool   `json:"accelHint,omitempty"`   // display the accelerator but leave the keystroke to the page; the only way to show a bare key like "Del"
	Disabled    bool   `json:"disabled,omitempty"`
	Checked     bool   `json:"checked,omitempty"`
	Separator   bool   `json:"separator,omitempty"`
	Submenu     []Item `json:"submenu,omitempty"`
	OnClick     func() `json:"-"`
}

type Menu []Item

func Separator() Item { return Item{Separator: true} }

func Std(role Role) Item { return Item{Role: role} }

func Sub(label string, items ...Item) Item { return Item{Label: label, Submenu: items} }

// Spec is what a role supplies for the fields an Item leaves blank.
type Spec struct {
	Label   string
	Accel   string
	Editing bool // the webview binds this keystroke natively; the accelerator is shown, not registered
}

func (r Role) Spec() (Spec, bool) {
	s, ok := roleSpecs[r]
	return s, ok
}

var redoAccel = func() string {
	if runtime.GOOS == "windows" {
		return "Ctrl+Y"
	}
	return "Ctrl+Shift+Z"
}()

var roleSpecs = map[Role]Spec{
	RoleQuit:             {Label: "Quit", Accel: "Ctrl+Q"},
	RoleAbout:            {Label: "About"},
	RoleUndo:             {Label: "Undo", Accel: "Ctrl+Z", Editing: true},
	RoleRedo:             {Label: "Redo", Accel: redoAccel, Editing: true},
	RoleCut:              {Label: "Cut", Accel: "Ctrl+X", Editing: true},
	RoleCopy:             {Label: "Copy", Accel: "Ctrl+C", Editing: true},
	RolePaste:            {Label: "Paste", Accel: "Ctrl+V", Editing: true},
	RoleSelectAll:        {Label: "Select All", Accel: "Ctrl+A", Editing: true},
	RoleMinimize:         {Label: "Minimize"},
	RoleClose:            {Label: "Close Window", Accel: "Ctrl+W"},
	RoleToggleFullscreen: {Label: "Toggle Full Screen", Accel: "F11"},
	RoleDevTools:         {Label: "Developer Tools", Accel: "F12"},
	RoleReload:           {Label: "Reload", Accel: "Ctrl+R"},
}
