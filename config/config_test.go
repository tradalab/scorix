package config

import "testing"

func TestAllowlistDecodesToMap(t *testing.T) {
	yaml := `
security:
  csp: default
  strict_modules: true
  allowlist:
    fs: true
    db: false
`
	cfg, err := FromBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	al := cfg.Security.Allowlist
	if !al["fs"] {
		t.Fatal(`allowlist["fs"] = false, want true`)
	}
	if al["db"] {
		t.Fatal(`allowlist["db"] = true, want false`)
	}
	if al["absent"] {
		t.Fatal("absent key must deny (fail closed)")
	}
	if !cfg.Security.StrictModules {
		t.Fatal("strict_modules not decoded")
	}
}

func TestRuntimeOverlayCannotEnableMCP(t *testing.T) {
	cfg, err := FromBytes([]byte("app:\n  name: sealed\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCORIX_MCP_ENABLED", "true")
	if err := ApplyOverlays(cfg, map[string]any{"mcp": map[string]any{"enabled": true}}); err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Enabled {
		t.Fatal("a runtime overlay turned mcp.enabled on")
	}
	built, err := FromBytes([]byte("mcp:\n  enabled: true\n"))
	if err != nil || !built.MCP.Enabled {
		t.Fatalf("the manifest's own mcp.enabled did not decode: %v", err)
	}
}

func TestDefaultCSPIsPreset(t *testing.T) {
	if got := DefaultConfig().Security.CSP; got != "default" {
		t.Fatalf("default CSP = %q, want the %q preset", got, "default")
	}
}
