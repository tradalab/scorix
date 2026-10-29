// Package share opens the system share sheet on Android; desktop reports unavailable.
package share

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/hostapi"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/native"
)

type Module struct{}

func New() *Module { return &Module{} }

func (m *Module) Name() string       { return "share" }
func (m *Module) Version() string    { return "1.1.0" }
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
	// Data is base64 in JSON; FileName and Mime are required with it.
	FileName string `json:"fileName,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

// The file crosses the IPC bridge as base64 in one message, so it stays small.
const maxFile = 10 << 20

// Share returns once the sheet is up: Android does not report whether anything was sent.
// JS: scorix.invoke("mod:share:Share", { text: "...", url: "https://..." })
// or { fileName: "chart.png", mime: "image/png", data: "<base64>" }
func (m *Module) Share(ctx context.Context, r Request) (string, error) {
	if r.Text == "" && r.URL == "" && len(r.Data) == 0 {
		return "", fault.New("invalid_request", "nothing to share: text, url and data are all empty")
	}
	if len(r.Data) > 0 {
		if r.Mime == "" || !validName(r.FileName) {
			return "", fault.New("invalid_request", "a file needs a mime type and a plain file name")
		}
		if len(r.Data) > maxFile {
			return "", fault.New("invalid_request", fmt.Sprintf("file is %d bytes, over the %d limit", len(r.Data), maxFile))
		}
	}
	c := &hostapi.ShareContent{Title: r.Title, Text: r.Text, Url: r.URL, DialogTitle: r.DialogTitle, FileName: r.FileName, Mime: r.Mime, Data: r.Data}
	if _, err := hostapi.Share.Share(ctx, c); err != nil {
		return "", mapErr(err)
	}
	return "ok", nil
}

// The name becomes a path segment of the provider URI, so it may not climb out of the share directory.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
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
