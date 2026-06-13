package window

type ID string

type Manager interface {
	// New must be called on the UI thread.
	New(opts Options) (Window, error)
	Get(id ID) (Window, bool)
	All() []Window
	Count() int
}
