package extension

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/tradalab/scorix/core/ipc/invoke"
	"github.com/tradalab/scorix/internal/ze"
)

func Expose(ext Extension, method string) {
	v := reflect.ValueOf(ext)
	m := v.MethodByName(method)
	if !m.IsValid() {
		panic("extension expose: method not found " + method)
	}

	mt := m.Type()

	handler := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args []reflect.Value

		switch mt.NumIn() {

		case 0:
			// method()
			// non ctx, non arg
			args = []reflect.Value{}

		case 1:
			// method(ctx) | method(arg)
			t0 := mt.In(0)

			if t0 == reflect.TypeOf((*context.Context)(nil)).Elem() {
				// method(ctx)
				args = []reflect.Value{reflect.ValueOf(ctx)}
			} else {
				// method(arg)
				argVal, err := ze.DecodeArg(raw, t0)
				if err != nil {
					return nil, err
				}
				args = []reflect.Value{argVal}
			}

		case 2:
			// method(ctx, arg)
			t0 := mt.In(0)
			t1 := mt.In(1)

			if t0 != reflect.TypeOf((*context.Context)(nil)).Elem() {
				panic("extension expose: first argument must be context.Context")
			}

			argVal, err := ze.DecodeArg(raw, t1)
			if err != nil {
				return nil, err
			}
			args = []reflect.Value{reflect.ValueOf(ctx), argVal}

		default:
			panic("extension expose: too many arguments")
		}

		// call method
		res := m.Call(args)

		// result, error
		if errVal := res[1]; !errVal.IsNil() {
			return nil, errVal.Interface().(error)
		}
		return res[0].Interface(), nil
	}

	invoke.Register("ext:"+ext.Name()+":"+method, handler)
}
