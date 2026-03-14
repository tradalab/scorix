// Package browser provides an OS browser integration module for scorix applications.
//
// Enable in app.yaml (no extra config required):
//
//	modules:
//	  browser:
//	    enabled: true
package browser

import (
	"context"
	"fmt"
	"github.com/tradalab/scorix/logger"
	"os/exec"
	"runtime"

	"github.com/tradalab/scorix/kernel/core/module"
)

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// BrowserModule provides functionality to open URLs in the native OS browser.
type BrowserModule struct{}

// New creates a new BrowserModule.
func New() *BrowserModule {
	return &BrowserModule{}
}

func (m *BrowserModule) Name() string    { return "browser" }
func (m *BrowserModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *BrowserModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[browser] loading (v%s)", m.Version()))

	// Register IPC handlers.
	module.Expose(m, "OpenUrl", ctx.IPC)

	return nil
}

func (m *BrowserModule) OnStart() error  { return nil }
func (m *BrowserModule) OnStop() error   { return nil }
func (m *BrowserModule) OnUnload() error { return nil }

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// OpenUrlRequest represents an IPC request to open a URL.
type OpenUrlRequest struct {
	URL string `json:"url"`
}

// OpenUrl opens a string URL in the default native OS browser.
// JS: scorix.invoke("mod:browser:OpenUrl", { url: "https://example.com" })
func (m *BrowserModule) OpenUrl(ctx context.Context, req interface{}) (interface{}, error) {
	// Support both {url: "str"} map struct and direct string from frontend.
	var url string
	switch v := req.(type) {
	case string:
		url = v
	case map[string]interface{}:
		if mappedUrl, ok := v["url"].(string); ok {
			url = mappedUrl
		} else {
			return nil, fmt.Errorf("missing 'url' key in request payload")
		}
	default:
		return nil, fmt.Errorf("invalid payload format, expected string or {url: string}")
	}

	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return nil, fmt.Errorf("unsupported platform")
	}

	return nil, cmd.Start()
}
