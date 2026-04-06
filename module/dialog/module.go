// Package dialog provides a native OS dialog integration module for scorix applications.
//
// Enable in app.yaml (no extra config required):
//
//	modules:
//	  dialog:
//	    enabled: true
package dialog

import (
	"context"
	"fmt"

	"github.com/sqweek/dialog"
	"github.com/tradalab/scorix/kernel/core/module"
	"github.com/tradalab/scorix/logger"
)

// Config holds the config block for this module.
// Fields are read from app.yaml → modules.dialog.*
type Config struct {
	Enabled bool `json:"enabled"`
}

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// DialogModule provides functionality to open native OS dialogs for file selection and messaging.
type DialogModule struct {
	ctx *module.Context
	cfg Config
}

// New creates a new DialogModule.
func New() *DialogModule {
	return &DialogModule{}
}

func (m *DialogModule) Name() string    { return "dialog" }
func (m *DialogModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *DialogModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[dialog] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	// Register IPC handlers.
	module.Expose(m, "OpenFile", ctx.IPC)
	module.Expose(m, "SaveFile", ctx.IPC)
	module.Expose(m, "Message", ctx.IPC)

	return nil
}

func (m *DialogModule) OnStart() error  { return nil }
func (m *DialogModule) OnStop() error   { return nil }
func (m *DialogModule) OnUnload() error { return nil }

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// OpenFileRequest represents an IPC request to open a file selection dialog.
type OpenFileRequest struct {
	Title  string `json:"title"`
	Filter string `json:"filter"` // Example pass "text files"
	Ext    string `json:"ext"`    // Example pass "txt"
}

// OpenFile opens a native OS dialog to select a file for opening.
// JS: scorix.invoke("mod:dialog:OpenFile", { title: "Select File", filter: "Text Files", ext: "txt" })
func (m *DialogModule) OpenFile(_ context.Context, req OpenFileRequest) (string, error) {
	b := dialog.File().Title(req.Title)
	if req.Filter != "" || req.Ext != "" {
		b = b.Filter(req.Filter, req.Ext)
	}

	path, err := b.Load()
	if err != nil {
		// sqweek/dialog can return "Cancelled" text on user abort.
		if err.Error() == "Cancelled" {
			return "", nil
		}
		return "", err
	}
	return path, nil
}

// SaveFileRequest represents an IPC request to open a file save dialog.
type SaveFileRequest struct {
	Title  string `json:"title"`
	Filter string `json:"filter"`
	Ext    string `json:"ext"`
}

// SaveFile opens a native OS dialog to select a path for saving a file.
// JS: scorix.invoke("mod:dialog:SaveFile", { title: "Save File", filter: "Text Files", ext: "txt" })
func (m *DialogModule) SaveFile(_ context.Context, req SaveFileRequest) (string, error) {
	b := dialog.File().Title(req.Title)
	if req.Filter != "" || req.Ext != "" {
		b = b.Filter(req.Filter, req.Ext)
	}

	path, err := b.Save()
	if err != nil {
		if err.Error() == "Cancelled" {
			return "", nil
		}
		return "", err
	}
	return path, nil
}

// MessageRequest represents an IPC request to open a native message box.
type MessageRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level"` // info, error
}

// Message opens a native OS alert or error message box.
// JS: scorix.invoke("mod:dialog:Message", { title: "Alert", text: "Something happened", level: "info" })
func (m *DialogModule) Message(_ context.Context, req MessageRequest) (string, error) {
	b := dialog.Message("%s", req.Text).Title(req.Title)
	if req.Level == "error" {
		b.Error()
	} else {
		b.Info()
	}
	return "ok", nil
}
