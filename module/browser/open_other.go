//go:build !windows

package browser

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func revealCmd(path string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path) // reveal in Finder, selected
	}
	return exec.Command("xdg-open", filepath.Dir(path))
}

func openPathCmd(path string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path)
	}
	return exec.Command("xdg-open", path)
}
