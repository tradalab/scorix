// Package clipboard is a native OS clipboard read/write module.
package clipboard

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"

	"github.com/atotto/clipboard"
	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	Enabled bool `json:"enabled"`
}

type ClipboardModule struct {
	ctx *module.Context
	cfg Config
}

func New() *ClipboardModule {
	return &ClipboardModule{}
}

func (m *ClipboardModule) Name() string    { return "clipboard" }
func (m *ClipboardModule) Version() string { return "1.0.0" }

func (m *ClipboardModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[clipboard] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	module.Expose(m, "Read", ctx.IPC)
	module.Expose(m, "Write", ctx.IPC)
	module.Expose(m, "ReadImage", ctx.IPC)
	module.Expose(m, "WriteImage", ctx.IPC)

	return nil
}

func (m *ClipboardModule) OnStart() error  { return nil }
func (m *ClipboardModule) OnStop() error   { return nil }
func (m *ClipboardModule) OnUnload() error { return nil }

// Read: scorix.invoke("mod:clipboard:Read")
func (m *ClipboardModule) Read(_ context.Context, _ struct{}) (string, error) {
	text, err := clipboard.ReadAll()
	if err != nil {
		return "", err
	}
	return text, nil
}

type WriteRequest struct {
	Text string `json:"text"`
}

// Write: scorix.invoke("mod:clipboard:Write", { text: "Sample text" })
func (m *ClipboardModule) Write(_ context.Context, req WriteRequest) (string, error) {
	err := clipboard.WriteAll(req.Text)
	if err != nil {
		return "", err
	}
	return "ok", nil
}

func (m *ClipboardModule) Capability() string { return "clipboard" }

type WriteImageRequest struct {
	PNG []byte `json:"png"` // base64 over the JSON envelope
}

func (m *ClipboardModule) WriteImage(_ context.Context, req WriteImageRequest) (string, error) {
	img, err := png.Decode(bytes.NewReader(req.PNG))
	if err != nil {
		return "", fault.Wrap("invalid_request", err)
	}
	if err := writeImage(img); err != nil {
		return "", err
	}
	return "ok", nil
}

type ReadImageResponse struct {
	PNG []byte `json:"png"`
}

func (m *ClipboardModule) ReadImage(_ context.Context, _ struct{}) (*ReadImageResponse, error) {
	img, err := readImage()
	if err != nil {
		return nil, err
	}
	data, err := encodePNGPortable(img)
	if err != nil {
		return nil, err
	}
	return &ReadImageResponse{PNG: data}, nil
}

func encodePNGPortable(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
