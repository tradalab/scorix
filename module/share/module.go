// Package share opens the system share sheet on Android; desktop reports unavailable.
package share

import (
	"context"
	"errors"
	"fmt"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/native"
)

type Module struct{}

func New() *Module { return &Module{} }

func (m *Module) Name() string       { return "share" }
func (m *Module) Version() string    { return "1.0.0" }
func (m *Module) Capability() string { return "share" }

func (m *Module) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[share] loading (v%s)", m.Version()))
	module.Expose(m, "Share", ctx.IPC)
	return nil
}

func (m *Module) OnStart() error  { return nil }
func (m *Module) OnStop() error   { return nil }
func (m *Module) OnUnload() error { return nil }

type Request struct {
	Title       string `json:"title,omitempty"`
	Text        string `json:"text,omitempty"`
	URL         string `json:"url,omitempty"`
	DialogTitle string `json:"dialogTitle,omitempty"`
}

// Share returns once the sheet is up: Android does not report whether anything was sent.
// JS: scorix.invoke("mod:share:Share", { text: "...", url: "https://..." })
func (m *Module) Share(ctx context.Context, r Request) (string, error) {
	if r.Text == "" && r.URL == "" {
		return "", fault.New("invalid_request", "nothing to share: text and url are both empty")
	}
	if err := native.Call(ctx, "share", "share", r, nil); err != nil {
		return "", mapErr(err)
	}
	return "ok", nil
}

func mapErr(err error) error {
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "sharing is not available on this platform")
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}
