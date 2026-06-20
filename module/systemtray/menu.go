package systemtray

// Separator set → renders a divider (Title/OnClick ignored); OnClick nil → display-only.
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
