// Package native calls plugins in the app's native host (dev.scorix.ScorixPlugin
// on Android); platform modules wrap a typed Go API around Call.
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
