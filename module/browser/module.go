// Package browser opens URLs in the OS browser over IPC.
package browser

import (
	"context"
	"fmt"
	neturl "net/url"
	"os/exec"
	"runtime"

	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/logger"
)

// allowedSchemes is the allow-list OpenUrl hands to the OS handler; anything
// else (file://, javascript:, custom protocols) is an RCE/local-file surface.
var allowedSchemes = map[string]bool{
	"http":   true,
	"https":  true,
	"mailto": true,
}

type BrowserModule struct{}

func New() *BrowserModule {
	return &BrowserModule{}
}

func (m *BrowserModule) Name() string    { return "browser" }
func (m *BrowserModule) Version() string { return "1.0.0" }

func (m *BrowserModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[browser] loading (v%s)", m.Version()))

	module.Expose(m, "OpenUrl", ctx.IPC)

	return nil
}

func (m *BrowserModule) OnStart() error  { return nil }
func (m *BrowserModule) OnStop() error   { return nil }
func (m *BrowserModule) OnUnload() error { return nil }

type OpenUrlRequest struct {
	URL string `json:"url"`
}

// OpenUrl opens a URL in the OS browser.
// JS: scorix.invoke("mod:browser:OpenUrl", { url: "https://example.com" })
func (m *BrowserModule) OpenUrl(ctx context.Context, req interface{}) (interface{}, error) {
	// Accept both a bare string and {url: "..."} from the frontend.
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

	// Validate before shelling out (see allowedSchemes).
	if url == "" {
		return nil, fmt.Errorf("url is empty")
	}
	parsed, err := neturl.Parse(url)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if !parsed.IsAbs() {
		return nil, fmt.Errorf("url must be absolute with an allowed scheme (http, https, mailto)")
	}
	if !allowedSchemes[parsed.Scheme] {
		return nil, fmt.Errorf("url scheme %q is not allowed (only http, https, mailto)", parsed.Scheme)
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
