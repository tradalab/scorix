package module

// Module is the lifecycle interface for a scorix module. Name and OnLoad are all it
// needs; it opts into the later phases by implementing Starter, Stopper or Unloader.
type Module interface {
	Name() string
	OnLoad(ctx *Context) error
}

// Starter runs after every module has loaded.
type Starter interface{ OnStart() error }

// Stopper runs at shutdown, in reverse registration order.
type Stopper interface{ OnStop() error }

// Unloader runs after every module has stopped.
type Unloader interface{ OnUnload() error }

type Capable interface {
	Capability() string
}
