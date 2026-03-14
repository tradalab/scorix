// Package template is a copy-paste starter template for a scorix module.
//
// # How to create a new module from this template
//
//  1. Copy this directory to module/<yourmodule>/
//  2. Rename the package: `package template` → `package <yourmodule>`
//  3. Rename the module path in go.mod
//  4. Rename TemplateModule → YourModule and update Name() / Version()
//  5. Add the module to go.work
//  6. Enable in app.yaml:
//     modules:
//     <yourmodule>:
//     enabled: true
//
// # IPC
// Methods exposed via module.Expose() become callable from JS:
//
//	scorix.invoke("<modulename>:<MethodName>", payload)
package template

import (
	"context"
	"log"

	"github.com/tradalab/scorix/kernel/core/module"
)

// Config holds the config block for this module.
// Fields are read from app.yaml → modules.<name>.*
type Config struct {
	// Add your config fields here.
	// Example:
	//   Timeout int    `json:"timeout"`
	//   DSN     string `json:"dsn"`
}

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// TemplateModule is a minimal scorix module skeleton.
// Rename to match your module's domain.
type TemplateModule struct {
	ctx *module.Context
	cfg Config
}

// New creates a new TemplateModule.
func New() *TemplateModule {
	return &TemplateModule{}
}

// Name is the unique module identifier. Must match the key in app.yaml.
func (m *TemplateModule) Name() string { return "template" }

// Version is the module's semantic version string.
func (m *TemplateModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

// OnLoad is called once when the module is loaded.
// - Decode config from ctx
// - Open connections / initialise resources
// - Register IPC handlers via module.Expose(m, "MethodName", ctx.IPC)
func (m *TemplateModule) OnLoad(ctx *module.Context) error {
	log.Printf("[%s] loading (v%s)", m.Name(), m.Version())

	m.ctx = ctx

	// Decode the full module config section into a typed struct.
	if err := ctx.Decode(&m.cfg); err != nil {
		return err
	}

	// TODO: initialise your resources here.
	// e.g. open a DB, connect to a service, etc.

	// Register IPC handlers:
	module.Expose(m, "Hello", ctx.IPC)
	// This binds Hello() as "template:Hello", callable from JS:
	//   scorix.invoke("template:Hello", payload)

	log.Printf("[%s] loaded", m.Name())
	return nil
}

// OnStart is called after all modules are loaded.
// Use it for tasks that depend on other modules being ready,
// or for starting background goroutines.
func (m *TemplateModule) OnStart() error {
	log.Printf("[%s] started", m.Name())
	// TODO: start background work here (goroutines, timers, etc.)
	return nil
}

// OnStop is called during graceful shutdown (reverse order of OnStart).
// Close connections, cancel contexts, stop goroutines.
func (m *TemplateModule) OnStop() error {
	log.Printf("[%s] stopping", m.Name())
	// TODO: stop background work, close resources.
	return nil
}

// OnUnload is called after OnStop.
// Release any remaining state.
func (m *TemplateModule) OnUnload() error {
	log.Printf("[%s] unloaded", m.Name())
	return nil
}

// ////////// IPC Handler ////////// ////////// ////////// ////////// ////////// //////////
// Each exported method below can be exposed as an IPC handler via
// module.Expose(m, "MethodName", ctx.IPC) inside OnLoad.
//
// Supported signatures:
//   Method() (Result, error)
//   Method(arg T) (Result, error)
//   Method(ctx context.Context) (Result, error)
//   Method(ctx context.Context, arg T) (Result, error)

// HelloRequest is an example IPC request payload.
type HelloRequest struct {
	Name string `json:"name"`
}

// HelloResponse is an example IPC response payload.
type HelloResponse struct {
	Message string `json:"message"`
}

// Hello is an example IPC handler.
// Expose with: module.Expose(m, "Hello", ctx.IPC)
// JS call:     scorix.invoke("template:Hello", { name: "World" })
func (m *TemplateModule) Hello(_ context.Context, req HelloRequest) (*HelloResponse, error) {
	name := req.Name
	if name == "" {
		name = "World"
	}
	return &HelloResponse{Message: "Hello, " + name + "!"}, nil
}
