package module

type State string

const (
	StateRegistered State = "registered"
	StateLoaded     State = "loaded"
	StateStarted    State = "started"
	StateStopped    State = "stopped"
)
