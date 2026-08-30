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

func TestDefaultCSPIsPreset(t *testing.T) {
	if got := DefaultConfig().Security.CSP; got != "default" {
		t.Fatalf("default CSP = %q, want the %q preset", got, "default")
	}
}
