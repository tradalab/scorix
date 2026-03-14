package module

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tradalab/scorix/kernel/internal/ipc"
)

const ipcKind = "command" // must match what the JS bridge sends (kind: "command")

// ModuleIPC provides a namespaced IPC surface for a single module.
// All handler names are registered as "command" kind, namespaced as
// "mod:<module>:<handlerName>" so they are addressable from JS via:
//
//	scorix.invoke("mod:gorm:Query", payload)
type ModuleIPC struct {
	moduleName string
	ipc        *ipc.IPC
}

// NewModuleIPC creates a module-scoped IPC wrapper.
func NewModuleIPC(name string, core *ipc.IPC) *ModuleIPC {
	return &ModuleIPC{
		moduleName: name,
		ipc:        core,
	}
}

// topic returns the fully-qualified IPC handler name "<module>:<name>".
func (m *ModuleIPC) topic(name string) string {
	return fmt.Sprintf("mod:%s:%s", m.moduleName, name)
}

// Handle registers a handler for the IPC command "mod:<module>:<name>".
// From JS: scorix.invoke("mod:<module>:<name>", payload)
func (m *ModuleIPC) Handle(name string, exec func(context.Context, json.RawMessage) (any, error)) {
	handler := ipc.Handler{
		Kind: ipcKind, // "command" — must match JS bridge
		Name: m.topic(name),
		Exec: exec,
	}
	m.ipc.AddHandlers([]ipc.Handler{handler})
}

// Invoke calls another IPC command handler directly from Go (Go→Go RPC).
func (m *ModuleIPC) Invoke(ctx context.Context, name string, payload any) (ipc.Message, error) {
	data, _ := json.Marshal(payload)
	msg := ipc.Message{
		Id:   ipc.GenerateId(),
		Kind: ipcKind,
		Name: name,
		Data: data,
	}
	return m.ipc.Invoke(ctx, msg)
}

// EmitEvent broadcasts an event to the frontend.
// Received in JS via: scorix.on("mod:<module>:<name>", handler)
func (m *ModuleIPC) EmitEvent(ctx context.Context, name string, payload any) error {
	data, _ := json.Marshal(payload)
	msg := ipc.Message{
		Id:   ipc.GenerateId(),
		Kind: "event",
		Name: m.topic(name),
		Data: data,
	}
	return m.ipc.Emit(ctx, msg)
}
