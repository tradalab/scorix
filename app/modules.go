package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

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

func (c *appController) OnOpenURL(fn func(string)) { c.a.OnOpenURL(fn) }

// Module registers a Scorix module (enabled by default). MUST be called before
// Run/RunWeb/Handler - modules load+start once at startup; later calls no-op (warned).
func (a *App) Module(m module.Module) {
	a.warnIfStarted("Module(" + m.Name() + ")")
	a.mods.Register(m)
	if _, ok := a.cfg.Modules[m.Name()]; !ok {
		a.cfg.Modules[m.Name()] = map[string]any{"enabled": true}
	}
	a.collectPermissions(m)
}

// What the names in security.allowlist mean for this module. One that does not
// split its surface gets a single set named after its capability, so an older
// allowlist keeps granting exactly what it granted.
func (a *App) collectPermissions(m module.Module) {
	a.permMu.Lock()
	defer a.permMu.Unlock()
	if a.permSets == nil {
		a.permSets = map[module.Permission][]module.Permission{}
	}
	pm, ok := m.(module.Permissioned)
	if !ok {
		if c, ok := m.(module.Capable); ok {
			cap := module.Permission(c.Capability())
			a.permSets[cap] = append(a.permSets[cap], cap)
		}
		return
	}
	set := pm.Permissions()
	if err := set.Validate(); err != nil {
		// A wiring mistake, like Expose naming a method that is not there.
		panic(fmt.Sprintf("module %s: %v", m.Name(), err))
	}
	for _, name := range set.Names() {
		for _, atom := range set.Expand(name) {
			if !slices.Contains(a.permSets[name], atom) {
				a.permSets[name] = append(a.permSets[name], atom)
			}
		}
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
		logger.Warn("app: "+call+" after Run/RunWeb/Handler - modules already started, this has no effect", "call", call)
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
	// Here rather than in Run so web mode gets it too, and because the socket
	// needs no window to be useful.
	a.startDevControl()
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
			logger.Info("app: module gate", "module", mod.Name(), "capability", capability, "allowed", a.permitted(module.Permission(capability)))
		}
	}
	a.permMu.RLock()
	for name := range a.permSets {
		declared[string(name)] = true
	}
	gated := slices.Clone(a.gated)
	a.permMu.RUnlock()
	for capability, on := range a.cfg.Security.Allowlist {
		if on && !declared[capability] {
			logger.Warn("app: security.allowlist enables a capability no registered module declares (typo?)", "capability", capability)
		}
	}
	// Every handler the manifest leaves shut, by name: otherwise the app starts,
	// the screen draws, and only the click comes back denied.
	for _, g := range gated {
		if !a.permitted(g.needs) {
			logger.Warn("app: handler denied by security.allowlist", "topic", g.topic, "permission", string(g.needs))
		}
	}
}

func (a *App) resetStarted() {
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
}

func (a *App) stopModules() {
	a.stopDevControl()
	a.mods.StopAll()
	a.mods.UnloadAll()
}

type moduleCore struct {
	reg *ipc.Registry
	app *App
}

var _ module.Core = (*moduleCore)(nil)

func (c *moduleCore) Register(name string, needs module.Permission, exec func(ctx context.Context, data json.RawMessage) (any, error)) {
	c.app.permMu.Lock()
	c.app.gated = append(c.app.gated, gatedHandler{topic: name, needs: needs})
	c.app.permMu.Unlock()
	c.reg.Command(name, func(ctx context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		// Asked every time: a handler registered before the manifest is applied
		// would otherwise cache a decision made too early.
		if !c.app.permitted(needs) {
			return nil, fault.Errorf(fault.CodeDenied, "permission %q denied by security.allowlist", needs).
				With("permission", string(needs))
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
