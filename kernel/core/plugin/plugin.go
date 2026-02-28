package plugin

import (
	"github.com/tradalab/scorix/kernel/core/config"
	"github.com/tradalab/scorix/kernel/core/messaging/command"
	"github.com/tradalab/scorix/kernel/core/messaging/event"
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
	Evt() *event.Event
	Cmd() *command.Command

	Cfg() *config.Config
	Store() *state.Store
}
