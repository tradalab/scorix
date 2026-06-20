package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/config"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/webview"
)

// moduleCapability maps a module to its gating allowlist capability; unlisted
// modules are not capability-gated.
var moduleCapability = map[string]string{
	"fs":           "fs",
	"clipboard":    "clipboard",
	"browser":      "shell", // opens URLs / shells out to the OS handler
	"dialog":       "shell",
	"notification": "notification",
	"updater":      "shell", // downloads + shell-executes a signed installer
}

// capabilityForCommand returns the capability gating a "mod:<module>:<method>"
// command, or "" if it isn't a gated module command.
func capabilityForCommand(name string) string {
	rest, ok := strings.CutPrefix(name, "mod:")
	if !ok {
		return ""
	}
	mod := rest
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		mod = rest[:i]
	}
	return moduleCapability[mod]
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
	return nil
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
	capability := capabilityForCommand(name)
	c.reg.Command(name, func(ctx context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		if capability != "" && !c.app.allowed(capability) {
			return nil, fmt.Errorf("capability %q denied by security.allowlist", capability)
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
