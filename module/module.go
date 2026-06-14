package module

// Module is the lifecycle interface for a scorix module.
type Module interface {
	Name() string
	Version() string

	OnLoad(ctx *Context) error

	// OnStart is called after all modules are loaded.
	OnStart() error

	// OnStop is called during graceful shutdown (reverse order).
	OnStop() error

	// OnUnload is called after OnStop to release resources.
	OnUnload() error
}
