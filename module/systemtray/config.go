package systemtray

// Config is modules.systemtray in scorix.yaml; both default to the app name.
type Config struct {
	Title   string `json:"title"`
	Tooltip string `json:"tooltip"`
}
