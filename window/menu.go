package window

// MenuItem is the driver-facing shape of a menu entry: roles are already
// resolved into OnClick by the app layer.
type MenuItem struct {
	Label     string
	Accel     Accel
	AccelHint bool // display the accelerator without binding it: the keystroke belongs to the page (or to the webview's own editing shortcuts)
	Disabled  bool
	Checked   bool
	Separator bool
	Submenu   []MenuItem
	OnClick   func() // runs on the UI thread
}

type MenuBarSetter interface {
	SetMenuBar(items []MenuItem) // nil clears the bar
}

type ContextMenuPopper interface {
	PopupMenu(items []MenuItem, x, y int) // logical client coords; negative = at the pointer
}

type EditCommand string

const (
	EditUndo      EditCommand = "undo"
	EditRedo      EditCommand = "redo"
	EditCut       EditCommand = "cut"
	EditCopy      EditCommand = "copy"
	EditPaste     EditCommand = "paste"
	EditSelectAll EditCommand = "selectAll"
)

type EditCommander interface {
	EditCommand(cmd EditCommand)
}
