package module

import (
	"context"
	"encoding/json"
	"fmt"
)

// Core is the IPC backend a module talks to. Plain types (no transport
// coupling) let the app implement it directly over its command/event Registry.
type Core interface {
	Register(name string, exec func(ctx context.Context, data json.RawMessage) (any, error))
	Invoke(ctx context.Context, name string, data json.RawMessage) (json.RawMessage, error)
	Emit(ctx context.Context, name string, data json.RawMessage) error
}

// ModuleIPC provides a namespaced IPC surface for a single module.
// Handler names are namespaced as "mod:<module>:<handlerName>", addressable from
// JS via scorix.invoke("mod:sqlx:Query", payload).
type ModuleIPC struct {
	moduleName string
	core       Core
}

func NewModuleIPC(name string, core Core) *ModuleIPC {
	return &ModuleIPC{moduleName: name, core: core}
}

func (m *ModuleIPC) topic(name string) string {
	return fmt.Sprintf("mod:%s:%s", m.moduleName, name)
}

// Handle registers a handler for the IPC command "mod:<module>:<name>".
func (m *ModuleIPC) Handle(name string, exec func(context.Context, json.RawMessage) (any, error)) {
	m.core.Register(m.topic(name), exec)
}

func (m *ModuleIPC) Invoke(ctx context.Context, name string, payload any) (json.RawMessage, error) {
	data, _ := json.Marshal(payload)
	return m.core.Invoke(ctx, name, data)
}

// EmitEvent broadcasts an event to the frontend.
// Received in JS via: scorix.on("mod:<module>:<name>", handler)
func (m *ModuleIPC) EmitEvent(ctx context.Context, name string, payload any) error {
	data, _ := json.Marshal(payload)
	return m.core.Emit(ctx, m.topic(name), data)
}
