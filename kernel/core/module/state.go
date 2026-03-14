package module

// State represents the lifecycle state of a module.
type State string

const (
	StateRegistered State = "registered"
	StateLoaded     State = "loaded"
	StateStarted    State = "started"
	StateStopped    State = "stopped"
)
