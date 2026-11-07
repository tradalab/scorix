package moduletest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type echoConfig struct {
	Prefix string    `json:"prefix"`
	Limits echoLimit `json:"limits"`
	Hidden string    `json:"-"`
	Plain  string
}

type echoLimit struct {
	Max int `json:"max"`
}

type echoIn struct {
	Text string `json:"text"`
}

type echo struct{ cfg echoConfig }

func (e *echo) Name() string       { return "echo" }
func (e *echo) Version() string    { return "0.0.1" }
func (e *echo) Capability() string { return "echo" }
func (e *echo) OnLoad(ctx *module.Context) error {
	if err := ctx.Decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.Prefix == "refuse" {
		return errors.New("refused prefix")
	}
	module.Expose(e, "Say", ctx.IPC)
	return nil
}
func (e *echo) OnStart() error  { return nil }
func (e *echo) OnStop() error   { return nil }
func (e *echo) OnUnload() error { return nil }
func (e *echo) Say(_ context.Context, in echoIn) (string, error) {
	return e.cfg.Prefix + in.Text, nil
}

func TestCallCrossesJSONAndTheDecodedConfig(t *testing.T) {
	r := Load(t, &echo{}, map[string]any{"prefix": "> "})
	out, err := r.Call("Say", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var said string
	if err := json.Unmarshal(out, &said); err != nil || said != "> hi" {
		t.Fatalf("Say = %s, want the configured prefix on the JSON payload", out)
	}
	if got := r.Needs("Say"); got != "echo" {
		t.Fatalf("Needs(Say) = %q, want the module capability", got)
	}
}

func TestTryLoadReturnsWhatLoadAllRefused(t *testing.T) {
	if _, err := TryLoad(t, &echo{}, map[string]any{"prefix": "refuse"}); err == nil || !strings.Contains(err.Error(), "refused prefix") {
		t.Fatalf("TryLoad err = %v, want the OnLoad error", err)
	}
}

func TestDataDirLivesUnderTheTestsTempHome(t *testing.T) {
	r := Load(t, &echo{}, nil)
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(r.DataDir(), home) || !strings.Contains(home, os.TempDir()) {
		t.Fatalf("DataDir %q, HOME %q: a module under test could write into the real home", r.DataDir(), home)
	}
}

func warned(key string) bool {
	return slices.ContainsFunc(logger.Tail(), func(line string) bool {
		return strings.Contains(line, "modules.echo") && strings.Contains(line, key) && strings.Contains(line, "unknown key")
	})
}

func TestAKeyNoFieldReadsIsReported(t *testing.T) {
	Load(t, &echo{}, map[string]any{
		"prefix": "> ",
		"prefx":  "typo",
		"limits": map[string]any{"max": 3, "maxx": 4},
		"Plain":  "untagged fields read their Go name",
	})
	if !warned(`"prefx"`) {
		t.Error("a misspelled top-level key went unreported")
	}
	if !warned(`"limits.maxx"`) {
		t.Error("a misspelled nested key went unreported")
	}
	for _, ok := range []string{`"prefix"`, `"enabled"`, `"limits"`, `"limits.max"`, `"Plain"`} {
		if warned(ok) {
			t.Errorf("%s is read, but was reported as unknown", ok)
		}
	}
}

func TestAFieldTaggedDashIsNotAKeyTheManifestCanSet(t *testing.T) {
	Load(t, &echo{}, map[string]any{"Hidden": "x"})
	if !warned(`"Hidden"`) {
		t.Error(`a key that only matches a json:"-" field went unreported`)
	}
}

type silent struct{}

func (silent) Name() string                     { return "silent" }
func (silent) Version() string                  { return "0.0.1" }
func (silent) Capability() string               { return "silent" }
func (silent) OnLoad(ctx *module.Context) error { return nil }
func (silent) OnStart() error                   { return nil }
func (silent) OnStop() error                    { return nil }
func (silent) OnUnload() error                  { return nil }

func TestKeysForAModuleThatReadsNoConfigAreReported(t *testing.T) {
	Load(t, silent{}, map[string]any{"title": "x"})
	found := slices.ContainsFunc(logger.Tail(), func(line string) bool {
		return strings.Contains(line, "modules.silent") && strings.Contains(line, `"title"`) && strings.Contains(line, "unknown key")
	})
	if !found {
		t.Error("a key under a module that never decodes its section went unreported")
	}
}

type quiet struct{ got string }

func (q *quiet) Name() string       { return "quiet" }
func (q *quiet) Capability() string { return "quiet" }
func (q *quiet) Version() string    { return "0.0.1" }
func (q *quiet) OnLoad(ctx *module.Context) error {
	module.Expose(q, "Note", ctx.IPC)
	module.Expose(q, "Fail", ctx.IPC)
	return nil
}
func (q *quiet) OnStart() error  { return nil }
func (q *quiet) OnStop() error   { return nil }
func (q *quiet) OnUnload() error { return nil }
func (q *quiet) Note(_ context.Context, in echoIn) error {
	q.got = in.Text
	return nil
}
func (q *quiet) Fail(context.Context) error { return errors.New("no") }

func TestAHandlerMayReturnOnlyAnError(t *testing.T) {
	q := &quiet{}
	r := Load(t, q, nil)
	out, err := r.Call("Note", echoIn{Text: "kept"})
	if err != nil || string(out) != "null" || q.got != "kept" {
		t.Fatalf("Note = %s, %v (got %q), want null with the argument delivered", out, err, q.got)
	}
	if _, err := r.Call("Fail", nil); err == nil || err.Error() != "no" {
		t.Fatalf("Fail err = %v, want the handler's own error", err)
	}
}

type bare struct{}

func (bare) Name() string                     { return "bare" }
func (bare) OnLoad(ctx *module.Context) error { return nil }

type counted struct {
	bare
	started, stopped, unloaded int
}

func (c *counted) Name() string    { return "counted" }
func (c *counted) OnStart() error  { c.started++; return nil }
func (c *counted) OnStop() error   { c.stopped++; return nil }
func (c *counted) OnUnload() error { c.unloaded++; return nil }

func TestAModuleNeedsOnlyANameAndOnLoad(t *testing.T) {
	Load(t, bare{}, nil).Lifecycle()
}

func TestEachLifecycleHookRunsWhenTheModuleHasIt(t *testing.T) {
	c := &counted{}
	Load(t, c, nil).Lifecycle()
	if c.started != 1 || c.stopped != 1 || c.unloaded != 1 {
		t.Fatalf("start %d stop %d unload %d, want each once", c.started, c.stopped, c.unloaded)
	}
}

type mode struct{ raw map[string]any }

func (m *mode) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &m.raw) }

type custom struct {
	cfg struct {
		Mode mode `json:"mode"`
	}
}

func (c *custom) Name() string { return "custom" }
func (c *custom) OnLoad(ctx *module.Context) error {
	return ctx.Decode(&c.cfg)
}

func TestAFieldThatDecodesItselfOwnsItsKeys(t *testing.T) {
	c := &custom{}
	Load(t, c, map[string]any{"mode": map[string]any{"anything": 1}})
	if c.cfg.Mode.raw["anything"] == nil {
		t.Fatal("the custom decoder never saw its key")
	}
	if slices.ContainsFunc(logger.Tail(), func(line string) bool {
		return strings.Contains(line, "modules.custom") && strings.Contains(line, "unknown key")
	}) {
		t.Error("a key read by the field's own UnmarshalJSON was reported as unknown")
	}
}
