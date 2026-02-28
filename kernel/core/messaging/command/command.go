package command

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tradalab/scorix/kernel/internal/ipc"
)

const Kind = "command"

type Handler func(context.Context, any) (any, error)

type Command struct {
	ipc *ipc.IPC
}

func New(core *ipc.IPC) *Command {
	return &Command{ipc: core}
}

func (c *Command) register(name string, fn any) error {
	hv := reflect.ValueOf(fn)
	ht := hv.Type()

	if ht.Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function")
	}

	if ht.NumIn() != 2 {
		return fmt.Errorf("handler must have 2 arguments")
	}

	if ht.NumOut() != 2 {
		return fmt.Errorf("handler must have 2 return values")
	}

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if !ht.In(0).Implements(ctxType) {
		return fmt.Errorf("first argument must implement context.Context")
	}

	if ht.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		panic("second return value must be error")
	}

	argType := ht.In(1)

	// build executor
	exec := c.buildExecutor(hv, argType)

	c.ipc.AddHandlers([]ipc.Handler{
		{
			Kind: Kind,
			Name: name,
			Exec: exec,
		},
	})

	return nil
}

func (c *Command) buildExecutor(hv reflect.Value, argType reflect.Type) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		// decode type
		argPtr := reflect.New(argType)

		if err := json.Unmarshal(raw, argPtr.Interface()); err != nil {
			return nil, err
		}

		argVal := argPtr.Elem()

		// safe assignable
		if !argVal.Type().AssignableTo(argType) {
			if argVal.Type().ConvertibleTo(argType) {
				argVal = argVal.Convert(argType)
			} else {
				return nil, fmt.Errorf("argument type mismatch")
			}
		}

		// safe call
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("handler panic:", r)
			}
		}()

		res := hv.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			argVal,
		})

		// return (value, error)
		if !res[1].IsNil() {
			return nil, res[1].Interface().(error)
		}
		return res[0].Interface(), nil
	}
}

func (c *Command) Handle(name string, fn any) {
	if err := c.register(name, fn); err != nil {
		panic(err)
	}
}

func (c *Command) Invoke(ctx context.Context, id string, name string, payload any) (json.RawMessage, error) {
	data, _ := json.Marshal(payload)
	msg := ipc.Message{
		Id:   id,
		Kind: Kind,
		Name: name,
		Data: data,
	}

	resp, err := c.ipc.Invoke(ctx, msg)
	if err != nil {
		return nil, err
	}

	if resp.State == ipc.StateError {
		return nil, fmt.Errorf(resp.Error)
	}

	return resp.Data, nil
}
