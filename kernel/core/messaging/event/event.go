package event

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tradalab/scorix/kernel/internal/ipc"
)

const Kind = "event"

type Handler func(context.Context, any)

type Event struct {
	ipc *ipc.IPC
}

func New(core *ipc.IPC) *Event {
	return &Event{ipc: core}
}

func (e *Event) register(name string, fn any) error {
	hv := reflect.ValueOf(fn)
	ht := hv.Type()

	if ht.Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function")
	}

	if ht.NumIn() != 2 {
		return fmt.Errorf("handler must have 2 arguments")
	}

	if ht.NumOut() != 0 {
		return fmt.Errorf("handler must not return values")
	}

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if !ht.In(0).Implements(ctxType) {
		return fmt.Errorf("first argument must implement context.Context")
	}

	argType := ht.In(1)

	// build executor 1 lần duy nhất
	exec := e.buildExecutor(hv, argType)

	e.ipc.AddHandlers([]ipc.Handler{
		{
			Kind: Kind,
			Name: name,
			Exec: exec,
		},
	})

	return nil
}

func (e *Event) buildExecutor(hv reflect.Value, argType reflect.Type) func(context.Context, json.RawMessage) (any, error) {
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

		hv.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			argVal,
		})

		return nil, nil
	}
}

func (e *Event) On(name string, fn any) {
	if err := e.register(name, fn); err != nil {
		panic(err)
	}
}

func (e *Event) Emit(ctx context.Context, id, name string, payload any) error {
	data, _ := json.Marshal(payload)
	msg := ipc.Message{
		Id:    id,
		Kind:  Kind,
		Name:  name,
		State: ipc.StateDispatch,
		Data:  data,
	}
	return e.ipc.Emit(ctx, msg)
}
