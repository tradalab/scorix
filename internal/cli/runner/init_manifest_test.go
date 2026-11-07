package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradalab/scorix/config"
)

func scaffoldManifest(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := writeTemplateFS("static/project", dir, map[string]string{"Name": "hello", "Package": "hello", "Shell": "vanilla-ts"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "scorix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.FromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestScaffoldRefusesModulesThatDeclareNoCapability(t *testing.T) {
	if !scaffoldManifest(t).Security.StrictModules {
		t.Fatal("a new app loads an ungated module with only a warning: security.strict_modules is off")
	}
}

func TestScaffoldGrantsNoCapabilityItRegistersNoModuleFor(t *testing.T) {
	cfg := scaffoldManifest(t)
	for name, sec := range cfg.Modules {
		if on, _ := config.AsStringMap(sec)["enabled"].(bool); on {
			t.Fatalf("scaffold now enables module %q: list what it grants here", name)
		}
	}
	for capability, on := range cfg.Security.Allowlist {
		if on {
			t.Errorf("security.allowlist grants %q but the scaffold registers no module declaring it", capability)
		}
	}
}

func TestScaffoldAllowlistExampleStaysValidYAMLWhenUncommented(t *testing.T) {
	dir := t.TempDir()
	if err := writeTemplateFS("static/project", dir, map[string]string{"Name": "hello", "Package": "hello", "Shell": "vanilla-ts"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "scorix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const example = "  #   notification: true\n"
	if !strings.Contains(string(raw), example) {
		t.Fatalf("the commented allowlist example is gone, or changed shape: update this test")
	}
	uncommented := strings.Replace(string(raw), example, "    notification: true\n", 1)
	cfg, err := config.FromBytes([]byte(uncommented))
	if err != nil {
		t.Fatalf("uncommenting the allowlist example breaks the manifest: %v", err)
	}
	if !cfg.Security.Allowlist["notification"] {
		t.Fatalf("allowlist = %v after uncommenting, want notification granted", cfg.Security.Allowlist)
	}
}
