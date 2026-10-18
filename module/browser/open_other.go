//go:build !windows && !android

package browser

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

func openURL(_ context.Context, url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	}
	return fmt.Errorf("unsupported platform")
}

func reveal(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start() // reveal in Finder, selected
	}
	return exec.Command("xdg-open", filepath.Dir(path)).Start()
}

func openPath(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}
