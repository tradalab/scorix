// Package template is a copy-paste starter skeleton for a scorix module.
package template

import (
	"context"
	"fmt"
	"github.com/tradalab/scorix/logger"

	"github.com/tradalab/scorix/module"
)

// Config holds the module config block; add your fields here.
type Config struct {
}

type TemplateModule struct {
	ctx *module.Context
	cfg Config
}

func New() *TemplateModule {
	return &TemplateModule{}
}

// Name must match the key in app.yaml.
func (m *TemplateModule) Name() string { return "template" }

func (m *TemplateModule) Version() string { return "1.0.0" }

func (m *TemplateModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[%s] loading (v%s)", m.Name(), m.Version()))

	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return err
	}

	// TODO: initialise your resources here.

	module.Expose(m, "Hello", ctx.IPC) // -> "mod:template:Hello"

	logger.Info(fmt.Sprintf("[%s] loaded", m.Name()))
	return nil
}

// OnStart starts background work after all modules are loaded.
func (m *TemplateModule) OnStart() error {
	logger.Info(fmt.Sprintf("[%s] started", m.Name()))
	// TODO: start background work here (goroutines, timers, etc.)
	return nil
}

// OnStop closes resources during graceful shutdown.
func (m *TemplateModule) OnStop() error {
	logger.Info(fmt.Sprintf("[%s] stopping", m.Name()))
	// TODO: stop background work, close resources.
	return nil
}

// OnUnload releases any remaining state after OnStop.
func (m *TemplateModule) OnUnload() error {
	logger.Info(fmt.Sprintf("[%s] unloaded", m.Name()))
	return nil
}

type HelloRequest struct {
	Name string `json:"name"`
}

type HelloResponse struct {
	Message string `json:"message"`
}

// Hello is an example IPC handler.
// JS: scorix.invoke("mod:template:Hello", { name: "World" })
func (m *TemplateModule) Hello(_ context.Context, req HelloRequest) (*HelloResponse, error) {
	name := req.Name
	if name == "" {
		name = "World"
	}
	return &HelloResponse{Message: "Hello, " + name + "!"}, nil
}
