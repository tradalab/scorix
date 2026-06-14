package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const checkProto = `
syntax = "proto3";
package demo;

message Empty {}
message PingReq {}
message PingRes { string status = 1; }

service healthz {
  rpc Ping (PingReq) returns (PingRes);
}
`

const checkSchema = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  created_at DATETIME,
  updated_at DATETIME,
  deleted_at DATETIME
);
`

func newCheckProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":          "module example.com/demo\n\ngo 1.26\n",
		"proto/app.proto": checkProto,
		"scorix.yaml":     "name: example.com/demo\nmodel:\n  schema: etc/schema.sql\n  dialect: sqlite\n",
		"etc/schema.sql":  checkSchema,
		"etc/app.yaml":    "app:\n  name: demo\nmodules:\n  fs:\n    enabled: true\n",
	}
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGenerateProto_Check(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()

	// 1. Check on a never-generated project: everything is missing → drift.
	err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true})
	if err == nil {
		t.Fatal("check on ungenerated project must report drift")
	}

	// 2. Generate, then check → clean. Nothing may be written by check itself.
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("check after generate must be clean: %v", err)
	}

	// 3. Hand-edit a force-regenerated file → drift naming that file.
	handler := filepath.Join(dir, "internal", "handler", "handler.go")
	orig, err := os.ReadFile(handler)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handler, append(orig, []byte("\n// tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	err = GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true})
	if err == nil || !strings.Contains(err.Error(), "out of sync") {
		t.Fatalf("tampered handler.go must drift, got: %v", err)
	}
	// Check must not have repaired the file (no writes in check mode).
	now, _ := os.ReadFile(handler)
	if !strings.Contains(string(now), "// tampered") {
		t.Fatal("check mode must not write")
	}
	if err := os.WriteFile(handler, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. Hand-edit a write-once file (app-owned) → check stays clean.
	mainGo := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\n\nfunc main() {} // app-owned edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("write-once edits must not drift: %v", err)
	}

	// 5. Edit the proto without regenerating → drift.
	protoPath := filepath.Join(dir, "proto", "app.proto")
	updated := strings.Replace(checkProto, "rpc Ping (PingReq) returns (PingRes);",
		"rpc Ping (PingReq) returns (PingRes);\n  rpc Pong (PingReq) returns (PingRes);", 1)
	if err := os.WriteFile(protoPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true}); err == nil {
		t.Fatal("proto edited without regen must drift")
	}
}

func TestGenerateProto_Check_CRLFNoFalseDrift(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Simulate a git autocrlf checkout: rewrite a generated file with CRLF.
	handler := filepath.Join(dir, "internal", "handler", "handler.go")
	b, err := os.ReadFile(handler)
	if err != nil {
		t.Fatal(err)
	}
	crlf := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n")
	if err := os.WriteFile(handler, []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("CRLF line endings must not be reported as drift: %v", err)
	}
}

func TestGenerateModel_Check(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()

	// Proto generation first: model patching needs internal/svc/service_context.go.
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("generate proto: %v", err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir, Schema: "etc/schema.sql"}); err != nil {
		t.Fatalf("generate model: %v", err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir, Schema: "etc/schema.sql", Check: true}); err != nil {
		t.Fatalf("check after generate must be clean: %v", err)
	}

	// Tamper the always-regenerated CRUD file → drift.
	gen := filepath.Join(dir, "internal", "model", "users_model_gen.go")
	b, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gen, append(b, []byte("\n// tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir, Schema: "etc/schema.sql", Check: true}); err == nil {
		t.Fatal("tampered model_gen.go must drift")
	}
	if err := os.WriteFile(gen, b, 0o644); err != nil {
		t.Fatal(err)
	}

	// Schema edited without regen (new column) → drift.
	schema := filepath.Join(dir, "etc", "schema.sql")
	updated := strings.Replace(checkSchema, "username TEXT NOT NULL UNIQUE,",
		"username TEXT NOT NULL UNIQUE,\n  email TEXT,", 1)
	if err := os.WriteFile(schema, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir, Schema: "etc/schema.sql", Check: true}); err == nil {
		t.Fatal("schema edited without regen must drift")
	}
}
