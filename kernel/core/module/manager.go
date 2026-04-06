package module

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/tradalab/scorix/kernel/core/config"
	"github.com/tradalab/scorix/kernel/internal/ipc"
	"github.com/tradalab/scorix/logger"
)

// Manager handles the full lifecycle of registered modules.
// Only modules enabled in config (modules.<name>.enabled: true) are loaded.
type Manager struct {
	registry *Registry
	cfg      *config.Config
	ipcCore  *ipc.IPC
	states   map[string]State
	order    []string // registration order
	mu       sync.RWMutex
	appCtrl  AppController
}

// NewManager creates a Manager wired with app config and IPC.
func NewManager(cfg *config.Config, ipcCore *ipc.IPC, appCtrl AppController) *Manager {
	return &Manager{
		registry: NewRegistry(),
		cfg:      cfg,
		ipcCore:  ipcCore,
		states:   make(map[string]State),
		appCtrl:  appCtrl,
	}
}

// Register adds a module to the manager. It does NOT load it yet.
func (m *Manager) Register(mod Module) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry.Register(mod)
	m.states[mod.Name()] = StateRegistered
	m.order = append(m.order, mod.Name())
}

// IsEnabled reports whether a module is enabled in config.
// A module is enabled when modules.<name>.enabled == true in app.yaml.
func (m *Manager) IsEnabled(name string) bool {
	if m.cfg == nil || m.cfg.Modules == nil {
		return false
	}
	entry, ok := toStringMap(m.cfg.Modules[name])
	if !ok {
		return false
	}
	enabled, _ := entry["enabled"].(bool)
	return enabled
}

// moduleSectionCfg returns the per-module config map for a given module name.
func (m *Manager) moduleSectionCfg(name string) map[string]any {
	if m.cfg == nil || m.cfg.Modules == nil {
		return nil
	}
	entry, ok := toStringMap(m.cfg.Modules[name])
	if !ok {
		return nil
	}
	return entry
}

// dataDir returns the platform-specific user data directory for the app.
func dataDir(appName string) string {
	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, appName)
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", appName)
		}
	default:
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, appName)
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", appName)
		}
	}
	return filepath.Join(os.TempDir(), appName, "data")
}

// Load loads a single module by name (only if enabled in config).
func (m *Manager) Load(name string) error {
	mod, ok := m.registry.Get(name)
	if !ok {
		return fmt.Errorf("module not found: %s", name)
	}

	if !m.IsEnabled(name) {
		logger.Info(fmt.Sprintf("[module] %s is disabled in config, skipping", name))
		return nil
	}

	appName := m.cfg.App.Name
	ctx := newContext(name, m.ipcCore, appName, dataDir(appName), m.moduleSectionCfg(name), m.appCtrl)

	if err := mod.OnLoad(ctx); err != nil {
		return fmt.Errorf("module %s OnLoad: %w", name, err)
	}

	m.mu.Lock()
	m.states[name] = StateLoaded
	m.mu.Unlock()

	logger.Info("[module] loaded: " + name)
	return nil
}

// LoadAll loads all registered modules that are enabled in config.
func (m *Manager) LoadAll() error {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for _, name := range order {
		if err := m.Load(name); err != nil {
			return err
		}
	}
	return nil
}

// Start starts a single loaded module.
func (m *Manager) Start(name string) error {
	mod, ok := m.registry.Get(name)
	if !ok {
		return fmt.Errorf("module not found: %s", name)
	}

	m.mu.RLock()
	state := m.states[name]
	m.mu.RUnlock()

	if state != StateLoaded {
		// not loaded (disabled or unregistered), skip silently
		return nil
	}

	if err := mod.OnStart(); err != nil {
		return fmt.Errorf("module %s OnStart: %w", name, err)
	}

	m.mu.Lock()
	m.states[name] = StateStarted
	m.mu.Unlock()

	logger.Info("[module] started: " + name)
	return nil
}

// StartAll starts all loaded modules in registration order.
func (m *Manager) StartAll() error {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for _, name := range order {
		if err := m.Start(name); err != nil {
			return err
		}
	}
	return nil
}

// Stop stops a single running module.
func (m *Manager) Stop(name string) error {
	mod, ok := m.registry.Get(name)
	if !ok {
		return fmt.Errorf("module not found: %s", name)
	}

	m.mu.RLock()
	state := m.states[name]
	m.mu.RUnlock()

	if state != StateStarted {
		return nil
	}

	if err := mod.OnStop(); err != nil {
		logger.Info(fmt.Sprintf("[module] %s OnStop error: %v", name, err))
	}

	m.mu.Lock()
	m.states[name] = StateStopped
	m.mu.Unlock()

	logger.Info("[module] stopped: " + name)
	return nil
}

// StopAll stops all started modules in reverse registration order.
func (m *Manager) StopAll() {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for i := len(order) - 1; i >= 0; i-- {
		_ = m.Stop(order[i])
	}
}

// Unload unloads a single stopped module.
func (m *Manager) Unload(name string) error {
	mod, ok := m.registry.Get(name)
	if !ok {
		return fmt.Errorf("module not found: %s", name)
	}

	m.mu.RLock()
	state := m.states[name]
	m.mu.RUnlock()

	if state == StateRegistered {
		// never loaded, nothing to do
		return nil
	}

	if err := mod.OnUnload(); err != nil {
		logger.Info(fmt.Sprintf("[module] %s OnUnload error: %v", name, err))
	}

	m.mu.Lock()
	delete(m.states, name)
	m.mu.Unlock()

	logger.Info("[module] unloaded: " + name)
	return nil
}

// UnloadAll unloads all modules in reverse registration order.
func (m *Manager) UnloadAll() {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for i := len(order) - 1; i >= 0; i-- {
		_ = m.Unload(order[i])
	}
}

// Get returns the raw Module by name.
func (m *Manager) Get(name string) (Module, bool) {
	return m.registry.Get(name)
}

// GetTyped returns a module cast to a concrete type T.
func GetTyped[T Module](mgr *Manager, name string) (T, bool) {
	mod, ok := mgr.registry.Get(name)
	if !ok {
		var zero T
		return zero, false
	}
	typed, ok := mod.(T)
	if !ok {
		var zero T
		return zero, false
	}
	return typed, true
}

// List returns all registered modules.
func (m *Manager) List() []Module {
	return m.registry.List()
}

// State returns the current lifecycle state of a module.
func (m *Manager) State(name string) State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[name]
}
