package ipc

import "encoding/json"

type Envelope struct {
	Type    string          `json:"type"` // "invoke" | "resolve" | "event"
	Payload json.RawMessage `json:"payload,omitempty"`
}

type InvokePayload struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type ResolvePayload struct {
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

type EventPayload struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}
