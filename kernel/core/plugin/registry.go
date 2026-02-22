package plugin

import (
	"sync"

	"github.com/tradalab/scorix/kernel/internal/logger"
)

type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
	App     App
}

func NewRegistry(app App) *Registry {
	return &Registry{
		plugins: make(map[string]Plugin),
		App:     app,
	}
}

func (r *Registry) Register(p Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Name()
	if _, exists := r.plugins[name]; exists {
		logger.Warn("plugin already registered", logger.Str("name", name))
		return
	}
	r.plugins[name] = p
	logger.Info("plugin registered", logger.Str("name", name), logger.Str("version", p.Version()))
}

func (r *Registry) StartAll() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, p := range r.plugins {
		ctx := Context{
			App:      r.App,
			Config:   r.App.Cfg().GetPluginConfig(p.Name()),
			Services: map[string]any{},
		}
		if err := p.Start(ctx); err != nil {
			logger.Error("plugin start failed", logger.Str("name", p.Name()), logger.Err(err))
			continue
		}
	}
	return nil
}
