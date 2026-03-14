// Package fs provides a file system paths module for scorix applications.
//
// Enable in app.yaml (no extra config required):
//
//	modules:
//	  fs:
//	    enabled: true
package fs

import (
	"context"
	"fmt"
	"github.com/tradalab/scorix/logger"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tradalab/scorix/kernel/core/module"
)

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// FSModule provides standard application paths to the frontend.
type FSModule struct {
	appName string
}

// New creates a new FSModule.
func New() *FSModule {
	return &FSModule{}
}

func (m *FSModule) Name() string    { return "fs" }
func (m *FSModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *FSModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[fs] loading (v%s)", m.Version()))

	// Grab the app name from the context to construct paths.
	m.appName = ctx.AppName
	if m.appName == "" {
		m.appName = "scorix-app"
	}

	// Register IPC handlers.
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

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// ConfigDir returns the path to the user config directory.
// JS: scorix.invoke("mod:fs:ConfigDir", null)
func (m *FSModule) ConfigDir(ctx context.Context) (string, error) {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, m.appName), nil
	}
	return filepath.Join(os.TempDir(), m.appName, "config"), nil
}

// DataDir returns the path to the application data directory.
// JS: scorix.invoke("mod:fs:DataDir", null)
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

// CacheDir returns the path to the user cache directory.
// JS: scorix.invoke("mod:fs:CacheDir", null)
func (m *FSModule) CacheDir(ctx context.Context) (string, error) {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, m.appName), nil
	}
	return filepath.Join(os.TempDir(), m.appName, "cache"), nil
}

// LogDir returns the path to the application log directory.
// JS: scorix.invoke("mod:fs:LogDir", null)
func (m *FSModule) LogDir(ctx context.Context) (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, m.appName, "logs"), nil
		}
	}
	cacheDir, _ := m.CacheDir(ctx)
	return filepath.Join(cacheDir, "logs"), nil
}

// TempDir returns a path to a temporary directory for the application.
// JS: scorix.invoke("mod:fs:TempDir", null)
func (m *FSModule) TempDir(ctx context.Context) (string, error) {
	return filepath.Join(os.TempDir(), m.appName), nil
}
