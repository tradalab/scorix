package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/cli/runner/dialect"
)

// stubSvcGo mirrors what the proto generator emits before scorix:model
// markers exist — patch flow should inject markers + populate them.
const stubSvcGo = `package svc

import (
	"context"
	"example.com/app/internal/config"
)

type ServiceContext struct {
	Cfg *config.Config
}

func NewServiceContext(cfg *config.Config) *ServiceContext {
	return &ServiceContext{
		Cfg: cfg,
	}
}
`

func writeStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPatchServiceContext_FreshProject(t *testing.T) {
	root := t.TempDir()
	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	writeStub(t, svcPath, stubSvcGo)

	tables := []sqlTable{
		{Name: "user", GoName: "User", TableName: "user"},
		{Name: "post", GoName: "Post", TableName: "post"},
	}
	if err := patchServiceContext(root, "example.com/app", tables, "example.com/app/etc", "etc"); err != nil {
		t.Fatalf("patch: %v", err)
	}

	got, err := os.ReadFile(svcPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)

	mustContain(t, out,
		`scorixsqlx "github.com/tradalab/scorix/module/sqlx"`,
		`"example.com/app/internal/model"`,
		`"example.com/app/etc"`,
		`UserModel model.UserModel`,
		`PostModel model.PostModel`,
		`sqlxMod := scorixsqlx.New(scorixsqlx.WithSchema(etc.SchemaSQL))`,
		`app.Modules().Register(sqlxMod)`,
		`UserModel: model.NewUserModel(sqlxMod.Conn),`,
		`PostModel: model.NewPostModel(sqlxMod.Conn),`,
	)
}

func TestPatchServiceContext_EmptyTables(t *testing.T) {
	root := t.TempDir()
	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	writeStub(t, svcPath, stubSvcGo)

	if err := patchServiceContext(root, "example.com/app", nil, "example.com/app/etc", "etc"); err != nil {
		t.Fatalf("patch: %v", err)
	}

	got, _ := os.ReadFile(svcPath)
	out := string(got)

	mustNotContain(t, out,
		"sqlxMod",
		`"github.com/tradalab/scorix/module/sqlx"`,
		"model.New",
	)
	// Markers must remain so the next non-empty regen can fill them.
	mustContain(t, out,
		"scorix:model:imports:start",
		"scorix:model:imports:end",
	)
}

func TestPatchAppYaml_FreshInsertSQLite(t *testing.T) {
	root := t.TempDir()
	yamlPath := filepath.Join(root, "etc", "app.yaml")
	writeStub(t, yamlPath, "app:\n  name: example\n")

	if err := patchAppYaml(root, dialect.MustNew("sqlite")); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := os.ReadFile(yamlPath)
	out := string(got)

	mustContain(t, out,
		"modules:",
		"  sqlx:",
		"    enabled: true",
		"    driver: sqlite3",
		"    dsn: app.dat",
	)
}

func TestPatchAppYaml_FreshInsertPostgres(t *testing.T) {
	root := t.TempDir()
	yamlPath := filepath.Join(root, "etc", "app.yaml")
	writeStub(t, yamlPath, "modules:\n  systray:\n    enabled: true\n")

	if err := patchAppYaml(root, dialect.MustNew("postgres")); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := os.ReadFile(yamlPath)
	out := string(got)

	mustContain(t, out,
		"  sqlx:",
		"    driver: pgx",
		"    dsn: postgres://user:pass@",
		// existing block survives
		"  systray:",
	)
}

func TestPatchAppYaml_FreshInsertMySQL(t *testing.T) {
	root := t.TempDir()
	yamlPath := filepath.Join(root, "etc", "app.yaml")
	writeStub(t, yamlPath, "name: example\n")

	if err := patchAppYaml(root, dialect.MustNew("mysql")); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := os.ReadFile(yamlPath)
	out := string(got)

	mustContain(t, out,
		"  sqlx:",
		"    driver: mysql",
		"    dsn: user:pass@tcp(127.0.0.1:3306)/app",
	)
}

func TestPatchAppYaml_IdempotentWhenSqlxAlreadyPresent(t *testing.T) {
	root := t.TempDir()
	yamlPath := filepath.Join(root, "etc", "app.yaml")
	original := "modules:\n  sqlx:\n    enabled: true\n    driver: pgx\n    dsn: my-custom-dsn\n"
	writeStub(t, yamlPath, original)

	if err := patchAppYaml(root, dialect.MustNew("sqlite")); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := os.ReadFile(yamlPath)
	if string(got) != original {
		t.Errorf("idempotent patch should not modify file. got:\n%s\nwant:\n%s", got, original)
	}
}
