package module

import (
	"context"
	"encoding/json"
	"fmt"
)

// Core is the IPC backend a module talks to; plain types keep it transport-agnostic.
type Core interface {
	Register(name string, exec func(ctx context.Context, data json.RawMessage) (any, error))
	Invoke(ctx context.Context, name string, data json.RawMessage) (json.RawMessage, error)
	Emit(ctx context.Context, name string, data json.RawMessage) error
}

// Handlers are namespaced "mod:<module>:<name>", e.g. JS scorix.invoke("mod:sqlx:Query", payload).
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

func (m *ModuleIPC) Handle(name string, exec func(context.Context, json.RawMessage) (any, error)) {
	m.core.Register(m.topic(name), exec)
}

func (m *ModuleIPC) Invoke(ctx context.Context, name string, payload any) (json.RawMessage, error) {
	data, _ := json.Marshal(payload)
	return m.core.Invoke(ctx, name, data)
}

// JS: scorix.on("mod:<module>:<name>", handler)
func (m *ModuleIPC) EmitEvent(ctx context.Context, name string, payload any) error {
	data, _ := json.Marshal(payload)
	return m.core.Emit(ctx, m.topic(name), data)
}
