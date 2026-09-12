package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func loadCheck(t *testing.T, yaml string) *CheckConfig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scorix.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg.Check
}

// Silence has to mean the check runs: an opt-in default is how apps reach CI
// with no frontend typecheck at all.
func TestTypecheckIsOnUntilAnAppSaysOtherwise(t *testing.T) {
	cases := map[string]bool{
		"app:\n  name: X\n":                       true,
		"app:\n  name: X\ncheck: {}\n":            true,
		"app:\n  name: X\ncheck:\n  lint: {}\n":   true,
		"check:\n  lint:\n    typecheck: true\n":  true,
		"check:\n  lint:\n    typecheck: false\n": false,
	}
	for src, want := range cases {
		if got := loadCheck(t, src).TypecheckEnabled(); got != want {
			t.Errorf("TypecheckEnabled() = %v, want %v for:\n%s", got, want, src)
		}
	}

	// The nil receiver is the manifest that predates this block entirely.
	var absent *CheckConfig
	if !absent.TypecheckEnabled() {
		t.Error("a manifest with no check block must still be typechecked")
	}
}

// A manifest that declares nothing must not make the runner ask for anything
// extra - the baseline has to stay the baseline.
func TestAbsentCheckBlockAddsNothing(t *testing.T) {
	var absent *CheckConfig
	if tc := absent.testCheck(); tc.Race || len(tc.Scripts) > 0 {
		t.Errorf("absent check block produced %+v", tc)
	}
	if s := absent.lintScripts(); len(s) > 0 {
		t.Errorf("absent check block produced lint scripts %v", s)
	}
}

func TestCheckBlockIsRead(t *testing.T) {
	c := loadCheck(t, `
check:
  test:
    race: true
    scripts: ["check:i18n"]
  lint:
    typecheck: false
    scripts: ["lint"]
`)
	tc := c.testCheck()
	if !tc.Race {
		t.Error("race was not read")
	}
	if len(tc.Scripts) != 1 || tc.Scripts[0] != "check:i18n" {
		t.Errorf("test scripts = %v", tc.Scripts)
	}
	if c.TypecheckEnabled() {
		t.Error("typecheck: false was not read")
	}
	if s := c.lintScripts(); len(s) != 1 || s[0] != "lint" {
		t.Errorf("lint scripts = %v", s)
	}
}
