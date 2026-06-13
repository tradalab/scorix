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

	// X, Y is the top-left position. nil means OS default; set Center to true
	// to center on the active monitor instead.
	X *int
	Y *int

	Center      bool
	Resizable   bool
	Frameless   bool
	Transparent bool
	AlwaysOnTop bool
	HideOnClose bool
	DevTools    bool

	// URL: initial content, typically an in-process scheme, e.g. "scorix://app/index.html".
	URL string
	// InitScript is injected before page scripts on every navigation.
	InitScript string
}

func DefaultOptions() Options {
	return Options{
		Width:     1024,
		Height:    768,
		Resizable: true,
		Center:    true,
	}
}
