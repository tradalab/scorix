package fault

import (
	"errors"
	"fmt"
)

const (
	CodeInternal    = "internal"    // handler panicked or an invariant broke
	CodeNotFound    = "not_found"   // no handler / no such resource
	CodeDenied      = "denied"      // blocked by security.allowlist or policy
	CodeCanceled    = "canceled"    // call canceled (client cancel, shutdown)
	CodeOverloaded  = "overloaded"  // concurrency budget exhausted, retry later
	CodeUnavailable = "unavailable" // transport/peer gone
)

type Error struct {
	Code    string
	Message string
	Details map[string]any

	cause error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.cause }

func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Errorf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

func Wrap(code string, err error) *Error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Message: err.Error(), cause: err}
}

func (e *Error) With(key string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = v
	return e
}

func CodeOf(err error) string {
	if fe, ok := errors.AsType[*Error](err); ok {
		return fe.Code
	}
	return ""
}
