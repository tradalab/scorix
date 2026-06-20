// Package dialog provides native OS dialogs (file/dir picker, message box) via
// ncruces/zenity (Win32 calls / osascript / zenity binary).
package dialog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ncruces/zenity"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	Enabled bool `json:"enabled"`
}

type DialogModule struct {
	ctx *module.Context
	cfg Config
}

func New() *DialogModule {
	return &DialogModule{}
}

func (m *DialogModule) Name() string    { return "dialog" }
func (m *DialogModule) Version() string { return "1.0.0" }

func (m *DialogModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[dialog] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	module.Expose(m, "OpenFile", ctx.IPC)
	module.Expose(m, "OpenDirectory", ctx.IPC)
	module.Expose(m, "SaveFile", ctx.IPC)
	module.Expose(m, "Message", ctx.IPC)

	return nil
}

func (m *DialogModule) OnStart() error  { return nil }
func (m *DialogModule) OnStop() error   { return nil }
func (m *DialogModule) OnUnload() error { return nil }

func fileFilters(name, ext string) []zenity.Option {
	if name == "" && ext == "" {
		return nil
	}
	pattern := "*"
	if ext != "" {
		pattern = "*." + ext
	}
	return []zenity.Option{zenity.FileFilters{{Name: name, Patterns: []string{pattern}, CaseFold: true}}}
}

// Maps user-Cancel to ("", nil) rather than an error (module contract).
func canceled(path string, err error) (string, error) {
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return path, err
}

type OpenFileRequest struct {
	Title  string `json:"title"`
	Filter string `json:"filter"`
	Ext    string `json:"ext"`
}

// JS: scorix.invoke("mod:dialog:OpenFile", { title: "Select File", filter: "Text Files", ext: "txt" })
func (m *DialogModule) OpenFile(_ context.Context, req OpenFileRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title)}
	opts = append(opts, fileFilters(req.Filter, req.Ext)...)
	return canceled(zenity.SelectFile(opts...))
}

type OpenDirectoryRequest struct {
	Title string `json:"title"`
	Dir   string `json:"dir,omitempty"`
}

// JS: scorix.invoke("mod:dialog:OpenDirectory", { title: "Pick folder", dir: "/Users/foo" })
func (m *DialogModule) OpenDirectory(_ context.Context, req OpenDirectoryRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title), zenity.Directory()}
	if req.Dir != "" {
		opts = append(opts, zenity.Filename(req.Dir))
	}
	return canceled(zenity.SelectFile(opts...))
}

type SaveFileRequest struct {
	Title    string `json:"title"`
	Filter   string `json:"filter"`
	Ext      string `json:"ext"`
	FileName string `json:"fileName,omitempty"`
	Dir      string `json:"dir,omitempty"`
}

// JS: scorix.invoke("mod:dialog:SaveFile", { title, filter, ext, fileName, dir })
func (m *DialogModule) SaveFile(_ context.Context, req SaveFileRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title), zenity.ConfirmOverwrite()}
	opts = append(opts, fileFilters(req.Filter, req.Ext)...)
	if req.Dir != "" || req.FileName != "" {
		opts = append(opts, zenity.Filename(filepath.Join(req.Dir, req.FileName)))
	}
	return canceled(zenity.SelectFileSave(opts...))
}

type MessageRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level"` // info, error
}

// JS: scorix.invoke("mod:dialog:Message", { title: "Alert", text: "Something happened", level: "info" })
func (m *DialogModule) Message(_ context.Context, req MessageRequest) (string, error) {
	var err error
	if req.Level == "error" {
		err = zenity.Error(req.Text, zenity.Title(req.Title))
	} else {
		err = zenity.Info(req.Text, zenity.Title(req.Title))
	}
	if err != nil && !errors.Is(err, zenity.ErrCanceled) {
		return "", err
	}
	return "ok", nil
}
