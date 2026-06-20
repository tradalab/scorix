// Package notification provides OS notifications (toast/banner) via ncruces/zenity.
// Gated by the "notification" capability.
package notification

import (
	"context"
	"fmt"

	"github.com/ncruces/zenity"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	Enabled bool `json:"enabled"`
}

type NotificationModule struct {
	ctx *module.Context
	cfg Config
}

func New() *NotificationModule {
	return &NotificationModule{}
}

func (m *NotificationModule) Name() string    { return "notification" }
func (m *NotificationModule) Version() string { return "1.0.0" }

func (m *NotificationModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[notification] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	module.Expose(m, "Notify", ctx.IPC)
	return nil
}

func (m *NotificationModule) OnStart() error  { return nil }
func (m *NotificationModule) OnStop() error   { return nil }
func (m *NotificationModule) OnUnload() error { return nil }

type NotifyRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level,omitempty"` // info (default) | warning | error — maps to the OS icon where supported
}

// JS: scorix.invoke("mod:notification:Notify", { title: "Build done", text: "All green" })
func (m *NotificationModule) Notify(_ context.Context, req NotifyRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title)}
	switch req.Level {
	case "warning":
		opts = append(opts, zenity.Icon(zenity.WarningIcon))
	case "error":
		opts = append(opts, zenity.Icon(zenity.ErrorIcon))
	default:
		opts = append(opts, zenity.Icon(zenity.InfoIcon))
	}
	if err := zenity.Notify(req.Text, opts...); err != nil {
		return "", err
	}
	return "ok", nil
}

// NotifyDirect notifies from Go without round-tripping the IPC registry.
func NotifyDirect(title, text string) error {
	return zenity.Notify(text, zenity.Title(title))
}
