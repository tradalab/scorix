package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tradalab/scorix/core/extension"
	"github.com/tradalab/scorix/internal/logger"
)

type StoreExt struct {
	mu       sync.RWMutex
	data     map[string]interface{}
	FilePath string
}

func (e *StoreExt) Name() string { return "store" }

func (e *StoreExt) Init(ctx context.Context) error {
	logger.Info("[store] init")

	e.data = make(map[string]interface{})

	return nil
}

func (e *StoreExt) Stop(ctx context.Context) error {
	logger.Info("[store] stop")

	// todo add FilePath
	// todo using DataDir from fs

	if err := e.saveToFile(); err != nil {
		logger.Error("[store] - error saving store data: " + err.Error())
		return err
	}
	e.mu.Lock()
	e.data = nil
	e.mu.Unlock()

	logger.Info("[store] - stopped")

	return nil
}

func (e *StoreExt) Set(args interface{}) (interface{}, error) {
	param, ok := args.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid params")
	}

	key, _ := param["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("missing key")
	}
	value := param["value"]

	e.mu.Lock()
	e.data[key] = value
	e.mu.Unlock()

	if err := e.saveToFile(); err != nil {
		logger.Error("Save failed: " + err.Error())
	}

	return "ok", nil
}

func (e *StoreExt) Get(args interface{}) (interface{}, error) {
	param, ok := args.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid params")
	}
	key, _ := param["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("missing key")
	}

	e.mu.RLock()
	value, exists := e.data[key]
	e.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("key not found")
	}
	return value, nil
}

func (e *StoreExt) Delete(args interface{}) (interface{}, error) {
	param, ok := args.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid params")
	}
	key, _ := param["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("missing key")
	}

	e.mu.Lock()
	delete(e.data, key)
	e.mu.Unlock()

	if err := e.saveToFile(); err != nil {
		logger.Error("Save failed: " + err.Error())
	}

	return "ok", nil
}

func (e *StoreExt) Keys(args interface{}) (interface{}, error) {
	e.mu.RLock()
	keys := make([]string, 0, len(e.data))
	for k := range e.data {
		keys = append(keys, k)
	}
	e.mu.RUnlock()
	return keys, nil
}

func (e *StoreExt) loadFromFile() error {
	if e.FilePath == "" {
		return fmt.Errorf("no file path provided")
	}

	if _, err := os.Stat(e.FilePath); os.IsNotExist(err) {
		return nil // file notfound → ignore
	}

	file, err := os.ReadFile(e.FilePath)
	if err != nil {
		return err
	}

	var m map[string]interface{}
	if err := json.Unmarshal(file, &m); err != nil {
		return err
	}

	e.mu.Lock()
	e.data = m
	e.mu.Unlock()
	return nil
}

func (e *StoreExt) saveToFile() error {
	if e.FilePath == "" {
		return fmt.Errorf("no file path provided")
	}

	dir := filepath.Dir(e.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	data, err := json.MarshalIndent(e.data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(e.FilePath, data, 0644)
}

func init() {
	extension.Register(&StoreExt{})
}
