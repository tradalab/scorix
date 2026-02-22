package kernel

import (
	"context"

	"github.com/tradalab/scorix/kernel/core/plugin"
)

type App interface {
	Run() error
	Expose(name string, handler any)
	Resolve(name string, params any)
	Emit(topic string, data any)
	On(topic string, handler func(data any)) func()

	Close()
	Show()

	plugin.App
}

// todo: using ExposeHandlerFunc
type ExposeHandlerFunc func(ctx context.Context, arg interface{}) (interface{}, error)
