package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tradalab/scorix/internal/appimage"
)

const (
	InstallSelf   = "self"
	InstallApt    = "apt"
	InstallManual = "manual"
)

type installEnv struct {
	goos     string
	exe      string
	appImage string
	appDir   string
	exists   func(path string) bool
}

var installEnvNow = currentInstallEnv

func currentInstallEnv() installEnv {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return installEnv{
		goos:     runtime.GOOS,
		exe:      exe,
		appImage: os.Getenv("APPIMAGE"),
		appDir:   os.Getenv("APPDIR"),
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
	}
}

func detectInstall(env installEnv) (kind, command string) {
	switch env.goos {
	case "windows":
		return InstallSelf, ""
	case "darwin":
		if strings.Contains(env.exe, ".app/Contents/MacOS/") {
			return InstallSelf, ""
		}
		return InstallManual, ""
	case "linux":
		if env.runningAppImage() != "" {
			return InstallSelf, ""
		}
		if dir, pkg := filepath.Split(env.exe); dir == "/usr/bin/" && pkg != "" && env.exists("/etc/apt/sources.list.d/"+pkg+".list") {
			return InstallApt, "sudo apt update && sudo apt install --only-upgrade " + pkg
		}
	}
	return InstallManual, ""
}

func (env installEnv) runningAppImage() string {
	image, _ := appimage.Of(env.exe, env.appImage, env.appDir)
	return image
}
