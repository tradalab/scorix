package moduletest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tradalab/scorix/module"
)

type echoConfig struct {
	Prefix string `json:"prefix"`
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
