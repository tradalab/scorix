package kernel

import (
	"github.com/tradalab/scorix/kernel/core/config"
	"github.com/tradalab/scorix/kernel/core/messaging/command"
	"github.com/tradalab/scorix/kernel/core/messaging/event"
	"github.com/tradalab/scorix/kernel/core/module"
)

type App interface {
	Run() error

	Evt() *event.Event
	Cmd() *command.Command

	// Modules returns the module manager so callers can register modules
	// before calling Run().
	Modules() *module.Manager

	Close()
	Show()
	Cfg() *config.Config
}
