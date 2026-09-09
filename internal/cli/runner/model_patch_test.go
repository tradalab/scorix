package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
)

// stubSvcGo mirrors what the proto generator emits before scorix:model
// markers exist - patch flow should inject markers + populate them.
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
	if err := patchServiceContext(root, "example.com/app", tables, "example.com/app/etc", "etc", "", "", dialect.SQLite{}); err != nil {
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
		`sqlxMod := scorixsqlx.New(scorixsqlx.WithSchema(etc.SchemaSQL), scorixsqlx.WithDriver("sqlite"))`,
		`sqlxMod.RegisterDriver("sqlite",`,
		`a.Module(sqlxMod)`,
		`UserModel: model.NewUserModel(sqlxMod.Conn),`,
		`PostModel: model.NewPostModel(sqlxMod.Conn),`,
	)
	// Runtime config is no longer hardcoded - the module self-defaults and reads
	// modules.sqlx / SCORIX_MODULE_SQLX_DSN at runtime (single source, no drift).
	mustNotContain(t, out, `a.SetModuleConfig("sqlx"`)
}

func TestPatchServiceContext_EmptyTables(t *testing.T) {
	root := t.TempDir()
	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	writeStub(t, svcPath, stubSvcGo)

	if err := patchServiceContext(root, "example.com/app", nil, "example.com/app/etc", "etc", "", "", dialect.SQLite{}); err != nil {
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

// An app with installs in the field cannot replay schema.sql: it never adds a
// column to a table that already exists. Declaring model.migrations swaps the
// generated wiring to goose, so such an app stops having to hand-edit the zone.
func TestPatchServiceContext_MigrationsInsteadOfSchema(t *testing.T) {
	root := t.TempDir()
	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	writeStub(t, svcPath, stubSvcGo)

	tables := []sqlTable{{Name: "user", GoName: "User", TableName: "user"}}
	if err := patchServiceContext(root, "loom", tables,
		"loom/etc", "etc", "loom/internal/migration", "migration", dialect.SQLite{}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	out, err := os.ReadFile(svcPath)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(out),
		`sqlxMod := scorixsqlx.New(scorixsqlx.WithMigrations(migration.FS, migration.Dir), scorixsqlx.WithDriver("sqlite"))`,
		`"loom/internal/migration"`,
		`UserModel: model.NewUserModel(sqlxMod.Conn),`,
	)
	// The two options are mutually exclusive - goose's version table fights
	// schema.sql's CREATE TABLE IF NOT EXISTS - so importing etc as well would
	// leave it unused and the file would not compile.
	mustNotContain(t, string(out), `WithSchema`, `"loom/etc"`)
}

// The driver the wiring registers and the package that registers it both follow
// model.dialect. Hardcoding sqlite meant a project declaring postgres got SQL
// with $1 placeholders and a connection opened against the wrong driver.
func TestPatchServiceContext_DriverFollowsDialect(t *testing.T) {
	cases := []struct {
		dialect      dialect.Dialect
		wantDriver   string
		wantImport   string
		unwantDriver string
	}{
		{dialect.SQLite{}, `RegisterDriver("sqlite"`, `_ "modernc.org/sqlite"`, `RegisterDriver("pgx"`},
		{dialect.MySQL{}, `RegisterDriver("mysql"`, `_ "github.com/go-sql-driver/mysql"`, `_ "modernc.org/sqlite"`},
		{dialect.Postgres{}, `RegisterDriver("pgx"`, `_ "github.com/jackc/pgx/v5/stdlib"`, `_ "modernc.org/sqlite"`},
	}
	for _, c := range cases {
		t.Run(c.dialect.Name(), func(t *testing.T) {
			root := t.TempDir()
			svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
			writeStub(t, svcPath, stubSvcGo)

			tables := []sqlTable{{Name: "user", GoName: "User", TableName: "user"}}
			if err := patchServiceContext(root, "example.com/app", tables,
				"example.com/app/etc", "etc", "", "", c.dialect); err != nil {
				t.Fatalf("patch: %v", err)
			}
			out, err := os.ReadFile(svcPath)
			if err != nil {
				t.Fatal(err)
			}
			mustContain(t, string(out), c.wantDriver, c.wantImport)
			mustNotContain(t, string(out), c.unwantDriver)
		})
	}
}
