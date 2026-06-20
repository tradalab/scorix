package module

// Module is the lifecycle interface for a scorix module.
type Module interface {
	Name() string
	Version() string

	OnLoad(ctx *Context) error
	OnStart() error // after all modules loaded
	OnStop() error  // graceful shutdown, reverse order
	OnUnload() error
}
