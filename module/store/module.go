// Package store is a key-value JSON-file storage module.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	Path string `json:"path"`
}

type StoreModule struct {
	mu       sync.RWMutex
	data     map[string]interface{}
	filePath string
	cfg      Config
}

func New() *StoreModule {
	return &StoreModule{
		data: make(map[string]interface{}),
	}
}

func (m *StoreModule) Name() string    { return "store" }
func (m *StoreModule) Version() string { return "1.0.0" }

func (m *StoreModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[store] loading (v%s)", m.Version()))

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	m.filePath = m.cfg.Path
	if m.filePath == "" {
		m.filePath = "store.json"
	}

	if !filepath.IsAbs(m.filePath) {
		m.filePath = filepath.Join(ctx.DataDir, m.filePath)
	}

	if err := m.loadFromFile(); err != nil {
		// Non-fatal: a fresh install has no file yet.
		logger.Error(fmt.Sprintf("[store] failed to load file %s: %v", m.filePath, err))
	} else {
		logger.Info(fmt.Sprintf("[store] loaded data from %s", m.filePath))
	}

	module.Expose(m, "Get", ctx.IPC)
	module.Expose(m, "Set", ctx.IPC)
	module.Expose(m, "Delete", ctx.IPC)
	module.Expose(m, "Keys", ctx.IPC)

	return nil
}

func (m *StoreModule) OnStart() error { return nil }

func (m *StoreModule) OnStop() error {
	logger.Info("[store] stopping, saving data...")
	if err := m.saveToFile(); err != nil {
		return fmt.Errorf("error saving store data: %w", err)
	}
	m.mu.Lock()
	m.data = nil
	m.mu.Unlock()
	return nil
}

func (m *StoreModule) OnUnload() error { return nil }

func (m *StoreModule) loadFromFile() error {
	if m.filePath == "" {
		return fmt.Errorf("no file path provided")
	}

	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		return nil
	}

	file, err := os.ReadFile(m.filePath)
	if err != nil {
		return err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(file, &data); err != nil {
		return err
	}

	m.mu.Lock()
	if data != nil {
		m.data = data
	}
	m.mu.Unlock()
	return nil
}

func (m *StoreModule) saveToFile() error {
	if m.filePath == "" {
		return fmt.Errorf("no file path provided")
	}

	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	dataBytes, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.filePath, dataBytes, 0644)
}

// Set: scorix.invoke("mod:store:Set", { key: "foo", value: "bar" })
func (m *StoreModule) Set(ctx context.Context, req map[string]interface{}) (interface{}, error) {
	key, _ := req["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("missing key")
	}
	value := req["value"]

	m.mu.Lock()
	m.data[key] = value
	m.mu.Unlock()

	// Surface persist failure rather than reporting "ok" (silent data loss).
	if err := m.saveToFile(); err != nil {
		logger.Error(fmt.Sprintf("[store] save failed: %v", err))
		return nil, fmt.Errorf("store: persist failed: %w", err)
	}

	return "ok", nil
}

// Get: scorix.invoke("mod:store:Get", { key: "foo" })
func (m *StoreModule) Get(ctx context.Context, req map[string]interface{}) (interface{}, error) {
	key, _ := req["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("missing key")
	}

	m.mu.RLock()
	value, exists := m.data[key]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("key not found")
	}
	return value, nil
}

// Delete: scorix.invoke("mod:store:Delete", { key: "foo" })
func (m *StoreModule) Delete(ctx context.Context, req map[string]interface{}) (interface{}, error) {
	key, _ := req["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("missing key")
	}

	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()

	// Surface persist failure rather than reporting "ok" (silent data loss).
	if err := m.saveToFile(); err != nil {
		logger.Error(fmt.Sprintf("[store] save failed: %v", err))
		return nil, fmt.Errorf("store: persist failed: %w", err)
	}

	return "ok", nil
}

// Keys: scorix.invoke("mod:store:Keys", null)
func (m *StoreModule) Keys(ctx context.Context) (interface{}, error) {
	m.mu.RLock()
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	m.mu.RUnlock()
	return keys, nil
}
