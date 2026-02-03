package extension

import (
	"fmt"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = make(map[string]Extension)
	order    []Extension
)

func Register(ext Extension) {
	if ext == nil {
		panic("extension.Register: nil extension")
	}
	if ext.Name() == "" {
		panic("extension.Register: extension name empty")
	}

	mu.Lock()
	defer mu.Unlock()

	if _, exists := registry[ext.Name()]; exists {
		panic(fmt.Sprintf("extension.Register: duplicate extension %s", ext.Name()))
	}

	registry[ext.Name()] = ext
	order = append(order, ext)
}

func All() []Extension {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]Extension, len(order))
	copy(out, order)
	return out
}

//func GetExt(name string) (Extension, bool) {
//	mu.RLock()
//	defer mu.RUnlock()
//	ext, ok := registry[name]
//	return ext, ok
//}

func GetExt[T Extension](name string) (T, bool) {
	mu.RLock()
	defer mu.RUnlock()

	ext, ok := registry[name]
	if !ok {
		var zero T
		return zero, false
	}

	typed, ok := ext.(T)
	if !ok {
		var zero T
		return zero, false
	}

	return typed, true
}
