package module

import (
	"fmt"
	"runtime/debug"

	"github.com/tradalab/scorix/logger"
)

// safeOnLoad/safeOnStart turn panics into errors (manager aborts startup);
// safeOnStop/safeOnUnload swallow them so shutdown always completes.

func safeOnLoad(mod Module, ctx *Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("[module] %s OnLoad panic: %v\n%s", mod.Name(), r, debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return mod.OnLoad(ctx)
}

func safeOnStart(mod Module) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("[module] %s OnStart panic: %v\n%s", mod.Name(), r, debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return mod.OnStart()
}

func safeOnStop(mod Module) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("[module] %s OnStop panic: %v\n%s", mod.Name(), r, debug.Stack()))
		}
	}()
	if err := mod.OnStop(); err != nil {
		logger.Error(fmt.Sprintf("[module] %s OnStop: %v", mod.Name(), err))
	}
}

func safeOnUnload(mod Module) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("[module] %s OnUnload panic: %v\n%s", mod.Name(), r, debug.Stack()))
		}
	}()
	if err := mod.OnUnload(); err != nil {
		logger.Error(fmt.Sprintf("[module] %s OnUnload: %v", mod.Name(), err))
	}
}
