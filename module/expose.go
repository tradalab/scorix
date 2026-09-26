package module

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tradalab/scorix/internal/ze"
)

var (
	ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errType = reflect.TypeOf((*error)(nil)).Elem()
)

// Expose binds Method([ctx context.Context][, arg T]) (R, error) at "mod:<module>:<Method>" via reflection.
// JS: scorix.invoke("mod:<module>:<Method>", payload).
//
// Without Needs the method asks for the module's whole capability, which is what
// every handler needed before permissions existed.
func Expose(mod Module, method string, mipc *ModuleIPC, opts ...ExposeOption) {
	v := reflect.ValueOf(mod)
	m := v.MethodByName(method)
	if !m.IsValid() {
		panic(fmt.Sprintf("module expose: method %q not found on %q", method, mod.Name()))
	}

	mt := m.Type()
	validateReturnSignature(method, mt)

	cfg := exposeCfg{needs: defaultPermission(mod)}
	for _, o := range opts {
		o(&cfg)
	}
	// A typo nothing downstream can catch: the app grants the set it knows
	// about and this one handler stays denied, both halves looking right.
	if pm, ok := mod.(Permissioned); ok && cfg.needs != "" {
		if set := pm.Permissions(); len(set.Atoms) > 0 && set.Expand(cfg.needs) == nil {
			panic(fmt.Sprintf("module expose: %q needs permission %q, which %q does not declare", method, cfg.needs, mod.Name()))
		}
	}

	handler := buildHandler(m, mt)
	mipc.Handle(method, cfg.needs, handler)
}

type exposeCfg struct{ needs Permission }

type ExposeOption func(*exposeCfg)

// Needs names the permission a handler asks for; it must be one its module
// declares in Permissions.
func Needs(p Permission) ExposeOption {
	return func(c *exposeCfg) { c.needs = p }
}

// A module that declares no capability at all is ungated; Manager.Load says so.
func defaultPermission(mod Module) Permission {
	if c, ok := mod.(Capable); ok {
		return Permission(c.Capability())
	}
	return ""
}

func validateReturnSignature(method string, mt reflect.Type) {
	if mt.NumOut() != 2 {
		panic(fmt.Sprintf("module expose: method %q must return (T, error), got %d return values", method, mt.NumOut()))
	}
	if !mt.Out(1).Implements(errType) {
		panic(fmt.Sprintf("module expose: method %q second return must be error", method))
	}
}

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
