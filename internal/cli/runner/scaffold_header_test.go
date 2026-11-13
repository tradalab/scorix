package runner

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tradalab/scorix/internal/cli/template"
)

const scaffoldHeader = "// Code scaffolded by scorix. Safe to edit."

var goGenerated = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

func TestAScaffoldedFileOpensWithTheScaffoldMarkerApartFromItsPackageAndAGeneratedOneWithDoNotEdit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "idl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idl", "app.proto"), []byte(eventsProto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("GenerateProto: %v", err)
	}

	lines := func(rel string) []string {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.SplitN(string(b), "\n", 3)
	}
	for _, rel := range []string{
		"main.go",
		"internal/config/config.go",
		"internal/svc/service_context.go",
		"internal/logic/healthz/ping_logic.go",
	} {
		l := lines(rel)
		if l[0] != scaffoldHeader || l[1] != "" {
			t.Errorf("%s opens with %q then %q, want %q then a blank line", rel, l[0], l[1], scaffoldHeader)
		}
		if goGenerated.MatchString(l[0]) {
			t.Errorf("%s is marked generated, so linters and reviews would skip code that is the app's own", rel)
		}
	}
	for _, rel := range []string{"internal/handler/handler.go", "internal/handler/modules.go"} {
		if l := lines(rel); !goGenerated.MatchString(l[0]) {
			t.Errorf("%s opens with %q, want a Go generated-code marker", rel, l[0])
		}
	}
	for name, path := range map[string]string{
		"model":      template.GoModel,
		"middleware": template.GoMiddleware,
		"page":       template.ShellPage,
	} {
		if !strings.HasPrefix(mustRead(path), scaffoldHeader+"\n\n") {
			t.Errorf("the %s template does not open with %q and a blank line", name, scaffoldHeader)
		}
	}
}
