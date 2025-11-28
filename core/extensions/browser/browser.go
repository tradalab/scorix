package browser

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/tradalab/scorix/core/extension"
	"github.com/tradalab/scorix/internal/logger"
)

type BrowserExt struct {
}

func (e *BrowserExt) Name() string {
	return "browser"
}

func (e *BrowserExt) Init(ctx context.Context) error {
	logger.Info("[browser] init")

	extension.Expose(e, "OpenUrl")

	return nil
}

func (e *BrowserExt) Stop(ctx context.Context) error {
	logger.Info("[browser] stop")
	return nil
}

func (e *BrowserExt) OpenUrl(ctx context.Context, url string) (interface{}, error) {
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

func init() {
	extension.Register(&BrowserExt{})
}
