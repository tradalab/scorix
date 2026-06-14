package ze

import (
	"encoding/json"
	"reflect"
)

func DecodeArg(raw json.RawMessage, argType reflect.Type) (reflect.Value, error) {
	argPtr := reflect.New(argType)

	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, argPtr.Interface()); err != nil {
			return reflect.Value{}, err
		}
	}

	if argType.Kind() == reflect.Ptr {
		return argPtr, nil
	}
	return argPtr.Elem(), nil
}
