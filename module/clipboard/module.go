// Package clipboard provides a native OS clipboard integration module for scorix applications.
//
// Enable in app.yaml (no extra config required):
//
//	modules:
//	  clipboard:
//	    enabled: true
package clipboard

import (
	"context"
	"fmt"

	"github.com/atotto/clipboard"
	"github.com/tradalab/scorix/kernel/core/module"
	"github.com/tradalab/scorix/logger"
)

// Config holds the config block for this module.
// Fields are read from app.yaml → modules.clipboard.*
type Config struct {
	Enabled bool `json:"enabled"`
}

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// ClipboardModule provides functionality to interact with the native OS clipboard.
type ClipboardModule struct {
	ctx *module.Context
	cfg Config
}

// New creates a new ClipboardModule.
func New() *ClipboardModule {
	return &ClipboardModule{}
}

func (m *ClipboardModule) Name() string    { return "clipboard" }
func (m *ClipboardModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *ClipboardModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[clipboard] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	// Register IPC handlers.
	module.Expose(m, "Read", ctx.IPC)
	module.Expose(m, "Write", ctx.IPC)

	return nil
}

func (m *ClipboardModule) OnStart() error  { return nil }
func (m *ClipboardModule) OnStop() error   { return nil }
func (m *ClipboardModule) OnUnload() error { return nil }

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// Read reads the text content from the native OS clipboard.
// JS: scorix.invoke("mod:clipboard:Read")
func (m *ClipboardModule) Read(_ context.Context, _ struct{}) (string, error) {
	text, err := clipboard.ReadAll()
	if err != nil {
		return "", err
	}
	return text, nil
}

// WriteRequest represents an IPC request to write text to the clipboard.
type WriteRequest struct {
	Text string `json:"text"`
}

// Write writes text content to the native OS clipboard.
// JS: scorix.invoke("mod:clipboard:Write", { text: "Sample text" })
func (m *ClipboardModule) Write(_ context.Context, req WriteRequest) (string, error) {
	err := clipboard.WriteAll(req.Text)
	if err != nil {
		return "", err
	}
	return "ok", nil
}
