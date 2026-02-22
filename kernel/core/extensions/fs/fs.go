package fs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tradalab/scorix/kernel/core/extension"
	"github.com/tradalab/scorix/kernel/internal/logger"
)

type FSExt struct {
	appName string
}

func (e *FSExt) Name() string { return "fs" }

func (e *FSExt) Init(ctx context.Context) error {
	logger.Info("[fs] init")
	if v, ok := extension.GetConfigPath(ctx, "app.name"); ok {
		e.appName = v.(string)
	}
	return nil
}

func (e *FSExt) Stop(ctx context.Context) error {
	logger.Info("[fs] stop")
	return nil
}

func (e *FSExt) ConfigDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, e.appName)
	}
	// fallback
	return filepath.Join(os.TempDir(), e.appName, "config")
}

func (e *FSExt) DataDir() string {
	// Windows: %AppData%
	// macOS: ~/Library/Application Support
	// Linux: ~/.local/share
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, e.appName)
		}
	} else if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", e.appName)
	} else {
		// Linux
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, e.appName)
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", e.appName)
	}
	return filepath.Join(os.TempDir(), e.appName, "data")
}

func (e *FSExt) CacheDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, e.appName)
	}
	return filepath.Join(os.TempDir(), e.appName, "cache")
}

func (e *FSExt) LogDir() string {
	if runtime.GOOS == "windows" {
		// %LocalAppData%\appName\logs
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, e.appName, "logs")
		}
	}
	// macOS/Linux -> dùng CacheDir + logs
	return filepath.Join(e.CacheDir(), "logs")
}

func (e *FSExt) TempDir() string {
	return filepath.Join(os.TempDir(), e.appName)
}

func init() {
	extension.Register(&FSExt{})
}
