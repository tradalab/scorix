package window

// Options zero values fall back to backend defaults; see DefaultOptions.
type Options struct {
	ID     ID
	Title  string
	Width  int
	Height int

	MinWidth  int
	MinHeight int
	MaxWidth  int
	MaxHeight int

	// X, Y top-left position; nil = OS default (or set Center to center instead).
	X *int
	Y *int

	Center      bool
	Resizable   bool
	Frameless   bool
	AlwaysOnTop bool
	HideOnClose bool
	DevTools    bool

	URL        string // initial content, e.g. "scorix://app/index.html"
	InitScript string // injected before page scripts on every navigation

	// IconPath is a native window icon (.ico); empty/missing uses the exe's own icon.
	IconPath string
}

func DefaultOptions() Options {
	return Options{
		Width:     1024,
		Height:    768,
		Resizable: true,
		Center:    true,
	}
}
