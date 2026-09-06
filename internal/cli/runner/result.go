package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Exit statuses. An agent branches on these instead of parsing prose, so they
// are part of the CLI contract: never renumber one, add instead.
const (
	ExitOK      = 0
	ExitFailed  = 1 // the command ran and failed
	ExitUsage   = 2 // the command was called wrong (bad flag or value)
	ExitDrift   = 3 // --check found generated code out of sync
	ExitMissing = 4 // a required tool is absent, nothing was attempted
)

// Anything not wrapped in ExitError is ExitFailed, so a runner reaches for this
// only when the caller can act on the difference.
type ExitError struct {
	Code int
	Kind string
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func exitErrorf(code int, kind, format string, a ...any) *ExitError {
	return &ExitError{Code: code, Kind: kind, Err: fmt.Errorf(format, a...)}
}

// UsageError is a caller mistake, not a run that failed: retrying it cannot help.
func UsageError(err error) *ExitError {
	return &ExitError{Code: ExitUsage, Kind: "usage", Err: err}
}

// jsonResult is the one envelope every --json command emits, so a caller can
// read ok/exit/kind without knowing which command it ran.
type jsonResult struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Exit    int    `json:"exit"`
	Kind    string `json:"kind,omitempty"`
	Error   string `json:"error,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// emitJSON writes the document and RETURNS THE ORIGINAL ERROR: the envelope
// reports a failure, it must not swallow the exit status that goes with it.
func emitJSON(w io.Writer, command string, data any, err error) error {
	res := jsonResult{Command: command, OK: err == nil, Data: data}
	if err != nil {
		res.Error = err.Error()
		res.Kind, res.Exit = "failed", ExitFailed
		var ee *ExitError
		if errors.As(err, &ee) {
			res.Kind, res.Exit = ee.Kind, ee.Code
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(res); encErr != nil && err == nil {
		return encErr
	}
	return err
}
