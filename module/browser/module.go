// Package browser opens URLs in the OS browser over IPC.
package browser

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

// Allow-list for OpenUrl; file://, javascript:, custom protocols are an RCE/local-file surface.
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
	module.Expose(m, "RevealPath", ctx.IPC)
	module.Expose(m, "OpenPath", ctx.IPC)

	return nil
}

func (m *BrowserModule) OnStart() error  { return nil }
func (m *BrowserModule) OnStop() error   { return nil }
func (m *BrowserModule) OnUnload() error { return nil }

type OpenUrlRequest struct {
	URL string `json:"url"`
}

// JS: scorix.invoke("mod:browser:OpenUrl", { url: "https://example.com" })
func (m *BrowserModule) OpenUrl(ctx context.Context, req interface{}) (interface{}, error) {
	// Accept both a bare string and {url: "..."}.
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

	// Validate before shelling out.
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

func (m *BrowserModule) Capability() string { return "shell" }

type PathRequest struct {
	Path string `json:"path"`
}

func (m *BrowserModule) RevealPath(_ context.Context, req PathRequest) (interface{}, error) {
	clean, err := validateLocalPath(req.Path)
	if err != nil {
		return nil, err
	}
	return nil, revealCmd(clean).Start()
}

func (m *BrowserModule) OpenPath(_ context.Context, req PathRequest) (interface{}, error) {
	clean, err := validateLocalPath(req.Path)
	if err != nil {
		return nil, err
	}
	return nil, openPathCmd(clean).Start()
}

func validateLocalPath(p string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(p))
	if clean == "" || clean == "." || !filepath.IsAbs(clean) {
		return "", fault.New("invalid_path", "path must be absolute")
	}
	if runtime.GOOS == "windows" && strings.HasPrefix(clean, `\\`) {
		return "", fault.New("invalid_path", "UNC paths are not allowed")
	}
	if _, err := os.Lstat(clean); err != nil {
		return "", fault.Wrap(fault.CodeNotFound, err)
	}
	return clean, nil
}
