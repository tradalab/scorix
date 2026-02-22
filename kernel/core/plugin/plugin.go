package plugin

import (
	"github.com/tradalab/scorix/kernel/core/config"
	"github.com/tradalab/scorix/kernel/core/state"
)

type Plugin interface {
	Name() string
	Version() string
	Start(ctx Context) error
	Stop() error
}

type Context struct {
	App      App
	Config   map[string]any
	Services map[string]any
}

type App interface {
	Expose(name string, handler any)
	Resolve(name string, params any)
	Emit(topic string, data any)
	On(topic string, handler func(data any)) func()

	Cfg() *config.Config
	Store() *state.Store
}
