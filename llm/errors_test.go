package llm

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

// "string too long" is how OpenAI refuses an over-long tool name, and an app
// reading that as a full window trims to nothing and retries for ever.
func TestIsContextFullKnowsAWindowFromAFieldLength(t *testing.T) {
	for _, s := range []string{
		"prompt is too long: 250000 tokens > 200000 maximum",
		"This model's maximum context length is 4096 tokens, however you requested 5000",
		"the request exceeds the available context size, try increasing it",
		"context_length_exceeded",
	} {
		if !IsContextFull(s) {
			t.Errorf("not read as a full window: %q", s)
		}
	}
	for _, s := range []string{
		"Invalid 'tools[0].function.name': string too long. Expected a string with maximum length 64",
		"Invalid value for 'name': string too long.",
		"stop sequence is too long",
		"Request too long",
	} {
		if IsContextFull(s) {
			t.Errorf("read as a full window: %q", s)
		}
	}
}

// An app decides whether to try again with errors.Is(err, ErrUnavailable),
// so a truncated answer must not read as garbage from the server.
func TestATruncatedReplyIsTheConnectionNotTheServer(t *testing.T) {
	var syntax, wrongType error
	if err := json.Unmarshal([]byte("}{"), &struct{}{}); err != nil {
		syntax = err
	}
	if err := json.Unmarshal([]byte(`{"n":"x"}`), &struct {
		N int `json:"n"`
	}{}); err != nil {
		wrongType = err
	}
	for _, tc := range []struct {
		name        string
		err         error
		unavailable bool
	}{
		{"a body that stopped halfway", io.ErrUnexpectedEOF, true},
		{"no body at all", io.EOF, true},
		{"a reset", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, true},
		{"a read that timed out", os.ErrDeadlineExceeded, true},
		{"JSON that is not JSON", syntax, false},
		{"a field of the wrong type", wrongType, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("the fixture produced no error")
			}
			err := Unreadable("p", "unreadable reply", tc.err)
			if got := errors.Is(err, ErrUnavailable); got != tc.unavailable {
				t.Errorf("ErrUnavailable = %v, want %v (%v)", got, tc.unavailable, err)
			}
			// The cause stays reachable either way, and the message still says
			// what could not be read.
			if !errors.Is(err, tc.err) {
				t.Errorf("the cause was dropped: %v", err)
			}
			if !strings.Contains(err.Error(), "unreadable reply") {
				t.Errorf("message = %q", err.Error())
			}
		})
	}
}
