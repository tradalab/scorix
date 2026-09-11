package runner

import "testing"

func TestResolveShellKindDefaultsToNextJS(t *testing.T) {
	k, err := resolveShellKind("")
	if err != nil {
		t.Fatal(err)
	}
	if k.Name != DefaultShellKind || k.Page != "app/page.tsx" {
		t.Fatalf("empty shell resolved to %+v", k)
	}
}

func TestResolveShellKindRejectsUnknown(t *testing.T) {
	if _, err := resolveShellKind("svelte"); err == nil {
		t.Fatal("an unknown shell was accepted, so a typo silently scaffolds nextjs")
	}
}

func TestOnlyThePageDiffersBetweenShells(t *testing.T) {
	seen := map[string]string{}
	for name, k := range shellKinds {
		if k.Name != name {
			t.Errorf("%s registered under key %s", k.Name, name)
		}
		if k.Name == "" {
			t.Errorf("%s has no name: %+v", name, k)
		}
		if k.Page != "" && k.Component == "" {
			t.Errorf("%s writes a page to %s but names no component", name, k.Page)
		}
		if k.Page == "" && k.React {
			t.Errorf("%s is React but declines the page: nothing would use hooks/events.ts", name)
		}
		if k.Page == "" {
			continue
		}
		if prev, dup := seen[k.Page]; dup {
			t.Errorf("%s and %s both write the page to %s", prev, name, k.Page)
		}
		seen[k.Page] = name
	}
	if len(shellKinds) < 2 {
		t.Skip("one shell registered: the seam is not exercised yet")
	}
}
