// Package native calls plugins in the app's native host (dev.scorix.ScorixPlugin
// on Android); platform modules wrap a typed Go API around Call.
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/nativecall"
)

// ErrUnavailable means this process has no native host: desktop, web mode, or
// an Android build without cgo.
var ErrUnavailable = errors.New("native: no native host on this platform")

// Code is the plugin's stable, machine-readable reason ("canceled", "unavailable").
type Error struct {
	Plugin, Method string
	Code           string
	Message        string
}

func (e *Error) Error() string {
	return fmt.Sprintf("native %s.%s: %s (%s)", e.Plugin, e.Method, e.Message, e.Code)
}

func IsCode(err error, code string) bool {
	var ne *Error
	return errors.As(err, &ne) && ne.Code == code
}

// Fault turns a Call error into the fault a page receives, with the plugin's code. The
// *Error stays inside it, so IsCode still answers on the result.
func Fault(err error) error {
	// Already a fault: the caller chose its code, and the plugin's must not replace it.
	if _, ok := errors.AsType[*fault.Error](err); ok {
		return err
	}
	if errors.Is(err, ErrUnavailable) {
		fe := fault.Wrap(fault.CodeUnavailable, err)
		fe.Message = "this runs only in the native host"
		return fe
	}
	if ne, ok := errors.AsType[*Error](err); ok {
		fe := fault.Wrap(ne.Code, err)
		fe.Message = ne.Message
		return fe
	}
	return err
}

// args are JSON-encoded, so []byte travels as base64. Canceling ctx also tells
// the plugin, which may dismiss whatever it is showing.
func Call(ctx context.Context, plugin, method string, args, out any) error {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		raw = b
	}
	r, err := nativecall.Call(ctx, plugin, method, raw)
	if errors.Is(err, nativecall.ErrNoHost) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	if !r.OK {
		return &Error{Plugin: plugin, Method: method, Code: r.Code, Message: r.Error}
	}
	if out == nil || len(r.Data) == 0 {
		return nil
	}
	return json.Unmarshal(r.Data, out)
}
