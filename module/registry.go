package module

import "sync"

// Registry stores registered modules in insertion order.
type Registry struct {
	modules map[string]Module
	order   []string
	mu      sync.RWMutex
}

func NewRegistry() *Registry {
	return &Registry{
		modules: make(map[string]Module),
	}
}

func (r *Registry) Register(m Module) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modules[m.Name()]; exists {
		panic("module: duplicate registration: " + m.Name())
	}
	r.modules[m.Name()] = m
	r.order = append(r.order, m.Name())
}

func (r *Registry) Get(name string) (Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[name]
	return m, ok
}

func (r *Registry) List() []Module {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Module, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.modules[name])
	}
	return out
}
