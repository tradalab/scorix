package kernel

import (
	"github.com/tradalab/scorix/kernel/core/messaging/command"
	"github.com/tradalab/scorix/kernel/core/messaging/event"
	"github.com/tradalab/scorix/kernel/core/plugin"
)

type App interface {
	Run() error

	Evt() *event.Event
	Cmd() *command.Command

	Close()
	Show()

	plugin.App
}
