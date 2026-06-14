package module

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/logger"
)

// Manager handles the lifecycle of registered modules; only modules with
// modules.<name>.enabled: true are loaded.
type Manager struct {
	registry *Registry
	cfg      *config.Config
	ipcCore  Core
	states   map[string]State
	order    []string
	mu       sync.RWMutex
	appCtrl  AppController
}

func NewManager(cfg *config.Config, ipcCore Core, appCtrl AppController) *Manager {
	return &Manager{
		registry: NewRegistry(),
		cfg:      cfg,
		ipcCore:  ipcCore,
		states:   make(map[string]State),
		appCtrl:  appCtrl,
	}
}

// Register does NOT load the module yet.
func (m *Manager) Register(mod Module) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry.Register(mod)
	m.states[mod.Name()] = StateRegistered
	m.order = append(m.order, mod.Name())
}

// IsEnabled reports whether modules.<name>.enabled == true in config.
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

func dataDir(appName string) string {
	if appName == "" {
		// Without a per-app subdir, modules would write into the platform data
		// root and collide; fall back to a fixed name.
		appName = "scorix-app"
	}
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

// Load is a no-op if the module is disabled in config.
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

	if err := safeOnLoad(mod, ctx); err != nil {
		return fmt.Errorf("module %s OnLoad: %w", name, err)
	}

	m.mu.Lock()
	m.states[name] = StateLoaded
	m.mu.Unlock()

	logger.Info("[module] loaded: " + name)
	return nil
}

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

func (m *Manager) Start(name string) error {
	mod, ok := m.registry.Get(name)
	if !ok {
		return fmt.Errorf("module not found: %s", name)
	}

	m.mu.RLock()
	state := m.states[name]
	m.mu.RUnlock()

	if state != StateLoaded {
		return nil
	}

	if err := safeOnStart(mod); err != nil {
		return fmt.Errorf("module %s OnStart: %w", name, err)
	}

	m.mu.Lock()
	m.states[name] = StateStarted
	m.mu.Unlock()

	logger.Info("[module] started: " + name)
	return nil
}

// StartAll starts modules in registration order.
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

	safeOnStop(mod)

	m.mu.Lock()
	m.states[name] = StateStopped
	m.mu.Unlock()

	logger.Info("[module] stopped: " + name)
	return nil
}

// StopAll stops modules in reverse registration order.
func (m *Manager) StopAll() {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for i := len(order) - 1; i >= 0; i-- {
		_ = m.Stop(order[i])
	}
}

func (m *Manager) Unload(name string) error {
	mod, ok := m.registry.Get(name)
	if !ok {
		return fmt.Errorf("module not found: %s", name)
	}

	m.mu.RLock()
	state := m.states[name]
	m.mu.RUnlock()

	if state == StateRegistered {
		return nil
	}

	safeOnUnload(mod)

	m.mu.Lock()
	delete(m.states, name)
	m.mu.Unlock()

	logger.Info("[module] unloaded: " + name)
	return nil
}

// UnloadAll unloads modules in reverse registration order.
func (m *Manager) UnloadAll() {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for i := len(order) - 1; i >= 0; i-- {
		_ = m.Unload(order[i])
	}
}

func (m *Manager) Get(name string) (Module, bool) {
	return m.registry.Get(name)
}

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

func (m *Manager) List() []Module {
	return m.registry.List()
}

func (m *Manager) State(name string) State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[name]
}
