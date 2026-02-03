package state

import (
	"sync"

	"github.com/tradalab/scorix/internal/logger"
)

type Store struct {
	mu   sync.RWMutex
	data map[string]any
	subs map[string][]func(any)
}

func New() *Store {
	return &Store{
		data: make(map[string]any),
		subs: make(map[string][]func(any)),
	}
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

	// Gọi ngay lần đầu
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
