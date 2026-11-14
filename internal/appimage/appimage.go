package appimage

import (
	"os"
	"path/filepath"
	"strings"
)

func Of(exe, image, dir string) (string, string) {
	dir = strings.TrimRight(dir, "/")
	if image == "" || dir == "" || !strings.HasPrefix(exe, dir+"/") {
		return "", ""
	}
	return image, dir
}

func Current() (string, string) {
	exe, err := os.Executable()
	if err != nil {
		return "", ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return Of(exe, os.Getenv("APPIMAGE"), os.Getenv("APPDIR"))
}
