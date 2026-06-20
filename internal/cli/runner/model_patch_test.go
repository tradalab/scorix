package runner

import (
	"os"
	"path/filepath"
	"testing"
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
		`"github.com/jmoiron/sqlx"`,
		`_ "modernc.org/sqlite"`,
		`"example.com/app/internal/model"`,
		`"example.com/app/etc"`,
		`UserModel model.UserModel`,
		`PostModel model.PostModel`,
		`sqlxMod := scorixsqlx.New(scorixsqlx.WithSchema(etc.SchemaSQL))`,
		`sqlxMod.RegisterDriver("sqlite",`,
		`a.Module(sqlxMod)`,
		`UserModel: model.NewUserModel(sqlxMod.Conn),`,
		`PostModel: model.NewPostModel(sqlxMod.Conn),`,
	)
	// Runtime config is no longer hardcoded — the module self-defaults and reads
	// modules.sqlx / SCORIX_MODULE_SQLX_DSN at runtime (single source, no drift).
	mustNotContain(t, out, `a.SetModuleConfig("sqlx"`)
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
