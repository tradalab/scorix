package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hand-arranged svc file importing model/etc OUTSIDE the marker zone used to
// get duplicates injected INSIDE it - generated code that does not compile.
func TestRenderServiceContext_NoDuplicateImports(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := `package svc

import (
	"loom/etc"
	"loom/internal/model"

	// scorix:model:imports:start
	// scorix:model:imports:end
)

type ServiceContext struct {
	// scorix:model:fields:start
	// scorix:model:fields:end
	_ = etc.SchemaSQL
	_ = model.Placeholder
}

func New() {
	// scorix:model:init:start
	// scorix:model:init:end
	// scorix:model:assigns:start
	// scorix:model:assigns:end
}
`
	if err := os.WriteFile(filepath.Join(root, "internal", "svc", "service_context.go"), []byte(svc), 0o644); err != nil {
		t.Fatal(err)
	}

	tables := []sqlTable{{Name: "docs", GoName: "Docs", TableName: "docs", PKGoName: "ID", PKGoType: "string", PKSqlNames: []string{"id"}}}
	_, content, err := renderServiceContext(root, "loom", tables, "loom/etc", "etc")
	if err != nil {
		t.Fatal(err)
	}
	out := string(content)
	if got := strings.Count(out, `"loom/internal/model"`); got != 1 {
		t.Fatalf("model imported %d times:\n%s", got, out)
	}
	if got := strings.Count(out, `"loom/etc"`); got != 1 {
		t.Fatalf("etc imported %d times:\n%s", got, out)
	}
	if !strings.Contains(out, `"github.com/jmoiron/sqlx"`) {
		t.Fatalf("zone lost its own imports:\n%s", out)
	}
}
