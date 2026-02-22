package ipc

import (
	"encoding/json"

	"github.com/tradalab/scorix/kernel/core/plugin"
)

type Envelope struct {
	Type    string          `json:"type"` // "invoke" | "resolve" | "event"
	Payload json.RawMessage `json:"payload,omitempty"`
}

type InvokePayload struct {
	Id     string          `json:"id"`
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

// hot dev
type App interface {
	Run() error
	Expose(name string, handler any)
	Resolve(name string, params any)
	Emit(topic string, data any)
	On(topic string, handler func(data any)) func()

	Close()
	Show()

	plugin.App
}
