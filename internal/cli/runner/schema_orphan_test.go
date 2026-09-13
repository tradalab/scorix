package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hazard this guards is the whole cost of moving a schema: the old
// schema_gen.go stays behind embedding a file that is gone, every scorix check
// keeps passing, and the app stops compiling with nothing to read.
func TestGenerateModelRefusesAStaleSchemaGen(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("scorix.yaml", "name: example.com/demo\nmodel:\n  schema: idl/schema.sql\n")
	write("idl/schema.sql", "CREATE TABLE t (id TEXT PRIMARY KEY);\n")
	write("etc/schema_gen.go", "package etc\n\nimport _ \"embed\"\n\n//go:embed schema.sql\nvar SchemaSQL string\n")

	err := GenerateModel(context.Background(), GenerateModelOptions{Dir: dir})
	if err == nil {
		t.Fatal("the stale etc/schema_gen.go was accepted, so the app breaks with nothing reporting it")
	}
	if !strings.Contains(err.Error(), "etc/schema_gen.go") {
		t.Fatalf("the error does not name the file to delete: %v", err)
	}

	// Deleting it must clear the refusal; whether the rest of generation then
	// succeeds depends on fixtures this test does not provide.
	if err := os.Remove(filepath.Join(dir, "etc", "schema_gen.go")); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(context.Background(), GenerateModelOptions{Dir: dir}); err != nil &&
		strings.Contains(err.Error(), "schema_gen.go") {
		t.Fatalf("still blamed the stale file after it was deleted: %v", err)
	}
}

// A schema_gen.go sitting next to its own schema.sql is the normal case and must
// not be mistaken for a leftover.
func TestGenerateModelAcceptsSchemaGenBesideItsSchema(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "idl"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"idl/schema.sql":    "CREATE TABLE t (id TEXT PRIMARY KEY);\n",
		"idl/schema_gen.go": "package idl\n\nimport _ \"embed\"\n\n//go:embed schema.sql\nvar SchemaSQL string\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := refuseOrphanSchemaGen(dir, filepath.Join(dir, "idl")); err != nil {
		t.Fatalf("the live schema_gen.go was reported as stale: %v", err)
	}
}
