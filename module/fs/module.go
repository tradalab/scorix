// Package fs exposes platform app paths (config/data/cache/log/temp) over IPC.
package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type FSModule struct {
	appName string
}

func New() *FSModule {
	return &FSModule{}
}

func (m *FSModule) Name() string    { return "fs" }
func (m *FSModule) Version() string { return "1.0.0" }

func (m *FSModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[fs] loading (v%s)", m.Version()))

	m.appName = ctx.AppName
	if m.appName == "" {
		m.appName = "scorix-app"
	}

	module.Expose(m, "ConfigDir", ctx.IPC)
	module.Expose(m, "DataDir", ctx.IPC)
	module.Expose(m, "CacheDir", ctx.IPC)
	module.Expose(m, "LogDir", ctx.IPC)
	module.Expose(m, "TempDir", ctx.IPC)

	return nil
}

func (m *FSModule) OnStart() error  { return nil }
func (m *FSModule) OnStop() error   { return nil }
func (m *FSModule) OnUnload() error { return nil }

// ConfigDir: scorix.invoke("mod:fs:ConfigDir", null)
func (m *FSModule) ConfigDir(ctx context.Context) (string, error) {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, m.appName), nil
	}
	return filepath.Join(os.TempDir(), m.appName, "config"), nil
}

// DataDir: scorix.invoke("mod:fs:DataDir", null)
func (m *FSModule) DataDir(ctx context.Context) (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, m.appName), nil
		}
	} else if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", m.appName), nil
		}
	} else {
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, m.appName), nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", m.appName), nil
		}
	}
	return filepath.Join(os.TempDir(), m.appName, "data"), nil
}

// CacheDir: scorix.invoke("mod:fs:CacheDir", null)
func (m *FSModule) CacheDir(ctx context.Context) (string, error) {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, m.appName), nil
	}
	return filepath.Join(os.TempDir(), m.appName, "cache"), nil
}

// LogDir: scorix.invoke("mod:fs:LogDir", null)
func (m *FSModule) LogDir(ctx context.Context) (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, m.appName, "logs"), nil
		}
	}
	cacheDir, _ := m.CacheDir(ctx)
	return filepath.Join(cacheDir, "logs"), nil
}

// TempDir: scorix.invoke("mod:fs:TempDir", null)
func (m *FSModule) TempDir(ctx context.Context) (string, error) {
	return filepath.Join(os.TempDir(), m.appName), nil
}
