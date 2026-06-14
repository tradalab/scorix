package webview

import "encoding/json"

// Message is the IPC envelope; State drives the request lifecycle including streaming and cancel
type Message struct {
	ID    string          `json:"id"`
	Kind  string          `json:"kind"` // command | event | module
	Name  string          `json:"name"`
	State string          `json:"state"`           // start | chunk | done | error | cancel
	Error string          `json:"error,omitempty"` // set when State == error
	Data  json.RawMessage `json:"data,omitempty"`
}
