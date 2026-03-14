package module

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tradalab/scorix/kernel/internal/ze"
)

var (
	ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errType = reflect.TypeOf((*error)(nil)).Elem()
)

// Expose binds a method on a module to its IPC surface using reflection.
// The handler is registered as "<module>:<Method>" and callable from JS via:
//
//	scorix.invoke("<module>:<Method>", payload)
//
// Supported method signatures:
//
//	Method() (R, error)
//	Method(arg T) (R, error)
//	Method(ctx context.Context) (R, error)
//	Method(ctx context.Context, arg T) (R, error)
func Expose(mod Module, method string, mipc *ModuleIPC) {
	v := reflect.ValueOf(mod)
	m := v.MethodByName(method)
	if !m.IsValid() {
		panic(fmt.Sprintf("module expose: method %q not found on %q", method, mod.Name()))
	}

	mt := m.Type()
	validateReturnSignature(method, mt)

	handler := buildHandler(m, mt)
	mipc.Handle(method, handler)
}

// validateReturnSignature ensures the method returns exactly (T, error).
func validateReturnSignature(method string, mt reflect.Type) {
	if mt.NumOut() != 2 {
		panic(fmt.Sprintf("module expose: method %q must return (T, error), got %d return values", method, mt.NumOut()))
	}
	if !mt.Out(1).Implements(errType) {
		panic(fmt.Sprintf("module expose: method %q second return must be error", method))
	}
}

// buildHandler creates the IPC executor function from a reflected method.
func buildHandler(m reflect.Value, mt reflect.Type) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		args, err := buildArgs(ctx, raw, mt)
		if err != nil {
			return nil, err
		}

		res := m.Call(args)

		if !res[1].IsNil() {
			return nil, res[1].Interface().(error)
		}
		return res[0].Interface(), nil
	}
}

// buildArgs resolves the argument list for the method call.
func buildArgs(ctx context.Context, raw json.RawMessage, mt reflect.Type) ([]reflect.Value, error) {
	switch mt.NumIn() {
	case 0:
		return []reflect.Value{}, nil

	case 1:
		t0 := mt.In(0)
		if t0.Implements(ctxType) {
			return []reflect.Value{reflect.ValueOf(ctx)}, nil
		}
		argVal, err := ze.DecodeArg(raw, t0)
		if err != nil {
			return nil, fmt.Errorf("decode arg: %w", err)
		}
		return []reflect.Value{argVal}, nil

	case 2:
		t0 := mt.In(0)
		if !t0.Implements(ctxType) {
			return nil, fmt.Errorf("expose: first param of 2-arg method must be context.Context")
		}
		t1 := mt.In(1)
		argVal, err := ze.DecodeArg(raw, t1)
		if err != nil {
			return nil, fmt.Errorf("decode arg: %w", err)
		}
		return []reflect.Value{reflect.ValueOf(ctx), argVal}, nil

	default:
		return nil, fmt.Errorf("expose: method has too many parameters (%d)", mt.NumIn())
	}
}
