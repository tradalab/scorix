package state

import (
	"encoding/json"
	"os"
	"sync"
	"path/filepath"

	"github.com/tradalab/scorix/logger"
)

type Store struct {
	mu       sync.RWMutex
	data     map[string]any
	subs     map[string][]func(any)
	savePath string
}

func New() *Store {
	return &Store{
		data: make(map[string]any),
		subs: make(map[string][]func(any)),
	}
}

// SetSavePath sets the file path where the state will be persisted.
func (s *Store) SetSavePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savePath = path
}

// Load reads the state from the JSON file at savePath.
func (s *Store) Load() error {
	s.mu.Lock()
	path := s.savePath
	s.mu.Unlock()

	if path == "" {
		return nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(b, &s.data)
}

// Save writes the state to the JSON file at savePath.
func (s *Store) Save() error {
	s.mu.RLock()
	path := s.savePath
	data := s.data
	s.mu.RUnlock()

	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0644)
}

// Set value + notify
func (s *Store) Set(key string, value any) {
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()

	logger.Info("state updated", logger.Str("key", key), logger.Any("value", value))
	for _, fn := range s.getSubs(key) {
		go fn(value) // async
	}
}

// Get value
func (s *Store) Get(key string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[key]
}

// On subscribe change
func (s *Store) On(key string, fn func(any)) func() {
	s.mu.Lock()
	s.subs[key] = append(s.subs[key], fn)
	s.mu.Unlock()

	// first call
	if v := s.Get(key); v != nil {
		go fn(v)
	}

	return func() { s.off(key, fn) }
}

func (s *Store) off(key string, fn func(any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.subs[key]
	for i, f := range subs {
		if &f == &fn {
			s.subs[key] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

func (s *Store) getSubs(key string) []func(any) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]func(any){}, s.subs[key]...)
}
