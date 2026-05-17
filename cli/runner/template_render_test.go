package runner

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradalab/scorix/cli/runner/dialect"
	"github.com/tradalab/scorix/cli/template"
)

// renderModelGen invokes the full template→go-format pipeline used by
// GenerateModel for one table, returning the rendered source. Failures from
// go/format.Source (called inside writeGeneratedFile) surface as test errors,
// proving the template produces syntactically valid Go.
func renderModelGen(t *testing.T, schema string, d dialect.Dialect) string {
	t.Helper()
	tables := parseInline(t, schema, d)
	if len(tables) == 0 {
		t.Fatal("no tables parsed")
	}
	tbl := tables[0]
	data := modelTemplateData{
		Module:  "example.com/app",
		Package: "model",
		Dialect: d,
		Table:   tbl,
		SQL:     buildSQL(tbl, d),
	}

	tplBody, err := template.ReadFile(template.GoModelGen)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	dst := filepath.Join(t.TempDir(), tbl.TableName+"_model_gen.go")
	if _, err := writeGeneratedFile(generatedFile{
		Path:     dst,
		Template: tplBody,
		Data:     data,
		Go:       true,
		Force:    true,
	}); err != nil {
		t.Fatalf("writeGeneratedFile: %v", err)
	}

	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRenderModelGen_SQLite(t *testing.T) {
	got := renderModelGen(t, connectionSchema, dialect.MustNew("sqlite"))

	mustContain(t, got,
		// imports
		`"context"`, `"database/sql"`, `"time"`,
		`"github.com/google/uuid"`,
		`"github.com/jmoiron/sqlx"`,
		// SQL constants exist (gofmt aligns the `=`, so check identifiers only)
		`connectionFindOneSQL`, `connectionFindAllSQL`,
		`connectionFindManySQL`, `connectionInsertSQL`,
		`connectionUpdateSQL`, `connectionDeleteSQL`,
		// Soft delete: DeleteSQL is an UPDATE, not DELETE FROM
		`= "UPDATE ` + "`connection` SET `deleted_at`",
		// SQLite ? placeholder
		"`id` = ?",
		// interface methods
		`Insert(ctx context.Context, data *Connection)`,
		`FindOne(ctx context.Context, id string)`,
		`FindMany(ctx context.Context, ids []string)`,
		`FindAll(ctx context.Context)`,
		`Update(ctx context.Context, data *Connection)`,
		`Delete(ctx context.Context, id string)`,
		// UUID hook
		`data.ID = uuid.NewString()`,
		// Soft delete passes time.Now() first arg
		`time.Now(), id`,
		// FindMany uses sqlx.In + Rebind, routed through scorixsqlx.From
		`sqlx.In(connectionFindManySQL, ids)`,
		`conn.Rebind(query)`,
		`scorixsqlx.From(ctx, m.conn)`,
		// Struct field db tags (gofmt-tolerant: identifier + tag fragment)
		"SshID",
		`db:"ssh_id"`,
		"DeletedAt",
		"sql.NullTime",
	)

	// Defence-in-depth: writeGeneratedFile already format.Sourced it, but
	// parser-only catches a different class of issue.
	_, err := parser.ParseFile(token.NewFileSet(), "out.go", got, parser.AllErrors)
	if err != nil {
		t.Fatalf("parser failed: %v\n\n%s", err, got)
	}
}

func TestRenderModelGen_Postgres(t *testing.T) {
	// Use Postgres-native types (TIMESTAMPTZ instead of DATETIME) so the
	// dialect type mapping resolves correctly.
	const schema = `
CREATE TABLE IF NOT EXISTS connection (
    id          TEXT PRIMARY KEY DEFAULT (gen_random_uuid()),
    name        TEXT NOT NULL DEFAULT '',
    port        BIGINT NOT NULL DEFAULT 6379,
    ssh_id      TEXT,
    created_at  TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ,
    deleted_at  TIMESTAMPTZ
);
`
	got := renderModelGen(t, schema, dialect.MustNew("postgres"))

	// Postgres SQL constants contain double-quoted identifiers, which appear
	// in source as backslash-escaped sequences inside %q-formatted literals.
	mustContain(t, got,
		`\"id\" = $1`,
		`\"deleted_at\" IS NULL`,
		// Sequential positional placeholders in INSERT/UPDATE.
		`VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		`WHERE \"id\" = $6`, // UPDATE WHERE pos = number-of-SET-cols + 1
		// FindMany STILL uses ? — sqlx.In + Rebind handles the conversion
		`IN (?)`,
		`sqlx.In(connectionFindManySQL, ids)`,
		`conn.Rebind(query)`,
		// time.Time for timestamps (correct mapping for TIMESTAMPTZ)
		`CreatedAt time.Time`,
	)

	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", got, parser.AllErrors); err != nil {
		t.Fatalf("parser failed: %v\n\n%s", err, got)
	}
}

func TestRenderModelGen_MySQL_HardDelete(t *testing.T) {
	const schema = `
CREATE TABLE IF NOT EXISTS account (
    id     INTEGER PRIMARY KEY,
    email  TEXT NOT NULL UNIQUE
);
`
	got := renderModelGen(t, schema, dialect.MustNew("mysql"))

	mustContain(t, got,
		// Hard delete (no deleted_at): DELETE FROM not UPDATE
		"= \"DELETE FROM `account`",
		// FindOneByEmail emitted from UNIQUE column
		`FindOneByEmail(ctx context.Context, email string)`,
	)

	mustNotContain(t, got,
		"uuid.NewString()",       // INTEGER PK, no UUID hook
		"time.Now(), id",         // no soft-delete time arg
		`"github.com/google/uuid"`,
	)

	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", got, parser.AllErrors); err != nil {
		t.Fatalf("parser failed: %v\n\n%s", err, got)
	}
}

func TestRenderModel_SqlxConstructor(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tables := parseInline(t, connectionSchema, d)
	tbl := tables[0]
	data := modelTemplateData{
		Module:  "example.com/app",
		Package: "model",
		Dialect: d,
		Table:   tbl,
		SQL:     buildSQL(tbl, d),
	}

	tplBody, err := template.ReadFile(template.GoModel)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "connection_model.go")
	if _, err := writeGeneratedFile(generatedFile{
		Path:     dst,
		Template: tplBody,
		Data:     data,
		Go:       true,
		Force:    true,
	}); err != nil {
		t.Fatalf("writeGeneratedFile: %v", err)
	}

	b, _ := os.ReadFile(dst)
	got := string(b)

	mustContain(t, got,
		`func NewConnectionModel(conn func() scorixsqlx.Conn) ConnectionModel`,
		`*defaultConnectionModel`,
		`customConnectionModel`,
	)
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q.\nFull output:\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if strings.Contains(got, w) {
			t.Errorf("output unexpectedly contains %q.\nFull output:\n%s", w, got)
		}
	}
}
