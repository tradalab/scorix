package module

// Module defines the lifecycle interface for a scorix module.
// Modules register their IPC handlers inside OnLoad via the provided Context.
type Module interface {
	Name() string
	Version() string

	// OnLoad is called once when the module is loaded.
	// The context carries IPC, config, and app metadata.
	OnLoad(ctx *Context) error

	// OnStart is called after all modules are loaded.
	OnStart() error

	// OnStop is called during graceful shutdown (reverse order).
	OnStop() error

	// OnUnload is called after OnStop to release resources.
	OnUnload() error
}
