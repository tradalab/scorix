package app

import (
	"testing"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/module/browser"
	"github.com/tradalab/scorix/module/dialog"
	"github.com/tradalab/scorix/module/updater"
)

func shellApp(t *testing.T, allow config.Allowlist) *App {
	t.Helper()
	a, err := New(Options{URL: "scorix://app/index.html", Security: &config.SandboxConfig{Allowlist: allow}})
	if err != nil {
		t.Fatal(err)
	}
	a.Module(browser.New())
	a.Module(dialog.New())
	a.Module(updater.New())
	return a
}

func TestShellIsASetOverThreeModulesThatEachGrantAlone(t *testing.T) {
	for _, c := range []struct {
		name  string
		allow config.Allowlist
		want  map[string]bool
	}{
		{"an old manifest granting shell", config.Allowlist{"shell": true}, map[string]bool{"browser": true, "dialog": true, "updater": true}},
		{"only the save dialog", config.Allowlist{"dialog": true}, map[string]bool{"browser": false, "dialog": true, "updater": false}},
		{"shell without opening files", config.Allowlist{"shell": true, "browser": false}, map[string]bool{"browser": false, "dialog": true, "updater": true}},
	} {
		a := shellApp(t, c.allow)
		for mod, want := range c.want {
			if got := a.permitted(module.Permission(mod)); got != want {
				t.Errorf("%s: %s permitted = %v, want %v", c.name, mod, got, want)
			}
		}
	}
}
