package systemtray

// MenuItem describes one tray menu entry. Zero-value semantics: an item with
// Separator set renders a divider (Title/OnClick ignored); OnClick may be nil
// for display-only entries.
type MenuItem struct {
	Title     string
	Tooltip   string
	OnClick   func()
	Separator bool
}

func Separator() MenuItem { return MenuItem{Separator: true} }

func Item(title string, onClick func()) MenuItem {
	return MenuItem{Title: title, OnClick: onClick}
}
