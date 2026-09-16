package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Apps lint their shell with eslint-plugin-prettier in their own style, which
// no generated file follows. Unless each file exempts itself, every app finds
// it the day the file first appears, as a red lint run: redishub got eleven
// prettier errors from hooks/events.ts and had to add it to its ignore list.
// The disable goes above "use client": a disable only covers the lines after
// it, so below the directive it left the directive's own semicolon reported,
// and a comment above a directive leaves it a directive (TypeScript and
// typescript-eslint both still read the prologue).
func TestGenerateProto_TSFilesExemptThemselvesFromESLint(t *testing.T) {
	const disable = "/* eslint-disable */"

	for _, tc := range []struct {
		shell     string
		directive string
		react     bool
	}{
		{shell: "nextjs", directive: `"use client";`, react: true},
		{shell: "vite-react", react: true},
		{shell: "vanilla-ts"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"go.mod":        "module example.com/demo\n\ngo 1.26\n",
				"idl/app.proto": eventsProto,
				"scorix.yaml":   "shell:\n  type: " + tc.shell + "\n",
			}
			for rel, body := range files {
				path := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
				t.Fatalf("GenerateProto: %v", err)
			}

			lines := func(rel string) []string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatalf("read %s: %v", rel, err)
				}
				return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
			}

			for _, rel := range []string{"shell/types/index.ts", "shell/api/index.ts"} {
				if got := lines(rel)[0]; got != disable {
					t.Errorf("%s starts with %q, want %q", rel, got, disable)
				}
			}

			hooksPath := filepath.Join(dir, "shell", "hooks", "events.ts")
			if !tc.react {
				if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
					t.Errorf("hooks/events.ts generated for a shell without React")
				}
				return
			}
			hooks := lines("shell/hooks/events.ts")
			if hooks[0] != disable {
				t.Errorf("hooks/events.ts starts with %q, want %q", hooks[0], disable)
			}
			// Also catches a manifest that silently fell back to nextjs.
			if tc.directive != "" && hooks[1] != tc.directive {
				t.Errorf("hooks/events.ts line 2 = %q, want the directive %q", hooks[1], tc.directive)
			}
			if tc.directive == "" && strings.Contains(hooks[1], "use client") {
				t.Errorf("hooks/events.ts carries a directive for %s, whose bundler warns on it", tc.shell)
			}
		})
	}
}
