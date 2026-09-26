package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// An app tells its user "check the API key" or "start the server" by branching
// on these with errors.Is, so providers must map their failures onto them.
var (
	ErrUnavailable   = errors.New("llm: provider unreachable")
	ErrUnauthorized  = errors.New("llm: credentials rejected")
	ErrModelNotFound = errors.New("llm: model not found")
	ErrRateLimited   = errors.New("llm: rate limited")

	ErrNoBinding       = errors.New("llm: slot has no provider bound")
	ErrUnknownProvider = errors.New("llm: unknown provider")
	ErrUnsupported     = errors.New("llm: provider lacks the capability")
	ErrNotLocal        = errors.New("llm: slot is local-only and the provider is not on this machine")
	ErrContextFull     = errors.New("llm: the conversation does not fit the context window")
)

// Kind is what an app branches on; Status and Message keep what the provider
// actually said, which the sentinel alone would lose.
type ProviderError struct {
	Provider string
	Status   int
	Message  string
	Kind     error
	// What actually failed, when a transport error started it. Kept because
	// retry.go has to ask whether it was a timeout, and Message is only text.
	Err error
}

func (e *ProviderError) Error() string {
	switch {
	case e.Status != 0 && e.Message != "":
		return fmt.Sprintf("llm %s: HTTP %d: %s", e.Provider, e.Status, e.Message)
	case e.Status != 0:
		return fmt.Sprintf("llm %s: HTTP %d", e.Provider, e.Status)
	default:
		return fmt.Sprintf("llm %s: %s", e.Provider, e.Message)
	}
}

// The sentinel an app branches on and the cause underneath.
func (e *ProviderError) Unwrap() []error {
	var out []error
	if e.Kind != nil {
		out = append(out, e.Kind)
	}
	if e.Err != nil {
		out = append(out, e.Err)
	}
	return out
}

// Unreadable is what a driver returns when a reply will not decode. A body that
// stopped halfway is the connection dropping, not the server talking nonsense,
// so it is marked ErrUnavailable and an app retrying on that sentinel retries
// it. Only a JSON grammar or type complaint is really garbage; anything else a
// decoder returns came from the reader underneath.
func Unreadable(provider, what string, err error) error {
	pe := &ProviderError{Provider: provider, Message: what + ": " + err.Error(), Err: err}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if !errors.As(err, &se) && !errors.As(err, &te) {
		pe.Kind = ErrUnavailable
	}
	return pe
}

// How the servers behind these drivers word a prompt that does not fit. One
// vocabulary rather than a copy per driver: an app trims and retries on
// ErrContextFull, so a driver that misses the wording leaves the conversation
// dead where the same app recovers on another.
func IsContextFull(msg string) bool {
	m := strings.ToLower(msg)
	// "prompt is too long" and not a bare "too long": OpenAI answers
	// "string too long" for a tool name over 64 characters, and an app that
	// reads that as a full window trims the conversation to nothing and
	// retries for ever while the one message that says what to fix is hidden.
	for _, s := range []string{"context_length_exceeded", "exceed_context_size", "maximum context length", "context window", "context size", "prompt is too long"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
