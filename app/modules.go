package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/fault"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/webview"
)

type appController struct{ a *App }

func (c *appController) Show()  { c.a.Show() }
func (c *appController) Close() { c.a.Quit() }

func (a *App) capabilityOf(name string) string {
	rest, ok := strings.CutPrefix(name, "mod:")
	if !ok {
		return ""
	}
	modName := rest
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		modName = rest[:i]
	}
	if mod, ok := a.mods.Get(modName); ok {
		if c, ok := mod.(module.Capable); ok {
			return c.Capability()
		}
	}
	return ""
}

// Module registers a Scorix module (enabled by default). MUST be called before
// Run/RunWeb/Handler — modules load+start once at startup; later calls no-op (warned).
func (a *App) Module(m module.Module) {
	a.warnIfStarted("Module(" + m.Name() + ")")
	a.mods.Register(m)
	if _, ok := a.cfg.Modules[m.Name()]; !ok {
		a.cfg.Modules[m.Name()] = map[string]any{"enabled": true}
	}
}

// SetModuleConfig merges cfg into a module's section (auto-enabled, caller keys
// win per-key; does NOT replace the whole section). Call before Module / Run.
func (a *App) SetModuleConfig(name string, cfg map[string]any) {
	a.warnIfStarted("SetModuleConfig(" + name + ")")
	section := config.AsStringMap(a.cfg.Modules[name])
	merged := make(map[string]any, len(section)+len(cfg)+1)
	for k, v := range section {
		merged[k] = v
	}
	for k, v := range cfg {
		merged[k] = v
	}
	merged["enabled"] = true
	a.cfg.Modules[name] = merged
}

func (a *App) warnIfStarted(call string) {
	a.mu.Lock()
	started := a.started
	a.mu.Unlock()
	if started {
		logger.Warn("app: "+call+" after Run/RunWeb/Handler — modules already started, this has no effect", "call", call)
	}
}

// startModules runs LoadAll+StartAll once (idempotent; Run and Handler both call it).
func (a *App) startModules() error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = true
	a.mu.Unlock()

	// On failure, unwind and clear started so a later call can retry.
	if err := a.mods.LoadAll(); err != nil {
		a.stopModules()
		a.resetStarted()
		return err
	}
	if err := a.mods.StartAll(); err != nil {
		a.stopModules()
		a.resetStarted()
		return err
	}
	a.auditAllowlist()
	return nil
}

func (a *App) auditAllowlist() {
	if a.opts.Security == nil {
		return
	}
	declared := map[string]bool{}
	for _, mod := range a.mods.List() {
		if c, ok := mod.(module.Capable); ok {
			capability := c.Capability()
			declared[capability] = true
			logger.Info("app: module gate", "module", mod.Name(), "capability", capability, "allowed", a.allowed(capability))
		}
	}
	for capability, on := range a.cfg.Security.Allowlist {
		if on && !declared[capability] {
			logger.Warn("app: security.allowlist enables a capability no registered module declares (typo?)", "capability", capability)
		}
	}
}

func (a *App) resetStarted() {
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
}

func (a *App) stopModules() {
	a.mods.StopAll()
	a.mods.UnloadAll()
}

type moduleCore struct {
	reg *ipc.Registry
	app *App
}

var _ module.Core = (*moduleCore)(nil)

func (c *moduleCore) Register(name string, exec func(ctx context.Context, data json.RawMessage) (any, error)) {
	capability := c.app.capabilityOf(name)
	c.reg.Command(name, func(ctx context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		if capability != "" && !c.app.allowed(capability) {
			return nil, fault.Errorf(fault.CodeDenied, "capability %q denied by security.allowlist", capability).
				With("capability", capability)
		}
		return exec(ctx, data)
	})
}

func (c *moduleCore) Invoke(ctx context.Context, name string, data json.RawMessage) (json.RawMessage, error) {
	return c.reg.Invoke(ctx, name, data)
}

func (c *moduleCore) Emit(_ context.Context, name string, data json.RawMessage) error {
	raw, err := json.Marshal(webview.Message{
		ID:    "mod-" + strconv.FormatUint(c.app.seq.Add(1), 10),
		Kind:  "event",
		Name:  name,
		State: "dispatch",
		Data:  data,
	})
	if err != nil {
		logger.Error("app: module emit marshal failed", "event", name, "err", err)
		return err
	}
	c.app.broadcast(raw)
	return nil
}
