package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
)

const testSchema = `
CREATE TABLE IF NOT EXISTS connection (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)))),
    name        TEXT NOT NULL DEFAULT '',
    port        INTEGER NOT NULL DEFAULT 6379,
    group_id    TEXT,
    ssh_id      TEXT,
    created_at  DATETIME,
    updated_at  DATETIME,
    deleted_at  DATETIME
);

CREATE TABLE IF NOT EXISTS setting (
    id     TEXT PRIMARY KEY,
    key    TEXT NOT NULL UNIQUE,
    value  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS membership (
    user_id  INTEGER NOT NULL,
    role_id  INTEGER NOT NULL,
    PRIMARY KEY (user_id, role_id),
    FOREIGN KEY (user_id) REFERENCES user(id),
    FOREIGN KEY (role_id) REFERENCES role(id)
);
`

func writeTempSchema(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseSQLSchema_SQLite(t *testing.T) {
	path := writeTempSchema(t, testSchema)
	tables, err := parseSQLSchema(path, dialect.MustNew("sqlite"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tables) != 3 {
		t.Fatalf("want 3 tables, got %d", len(tables))
	}

	// --- connection ---
	conn := tables[0]
	if conn.Name != "connection" || conn.GoName != "Connection" {
		t.Errorf("connection naming wrong: %+v", conn)
	}
	if !conn.HasTime {
		t.Error("connection should have time import (has DATETIME cols)")
	}
	if !conn.HasNullable {
		t.Error("connection should have nullable import (deleted_at is nullable DATETIME → sql.NullTime)")
	}
	if !conn.HasDeletedAt {
		t.Error("connection should have soft-delete flag")
	}
	if conn.PKGoName != "ID" || conn.PKGoType != "string" {
		t.Errorf("connection PK = %s %s, want ID string", conn.PKGoName, conn.PKGoType)
	}
	// group_id should map to GroupID (Go convention)
	var groupCol *sqlColumn
	for i := range conn.Columns {
		if conn.Columns[i].Name == "group_id" {
			groupCol = &conn.Columns[i]
		}
	}
	if groupCol == nil {
		t.Fatal("group_id column missing")
	}
	if groupCol.GoName != "GroupID" {
		t.Errorf("group_id GoName = %q, want GroupID", groupCol.GoName)
	}
	// nullable DATETIME → sql.NullTime; non-null DATETIME → time.Time
	var delCol *sqlColumn
	for i := range conn.Columns {
		if conn.Columns[i].Name == "deleted_at" {
			delCol = &conn.Columns[i]
		}
	}
	if delCol == nil || delCol.GoType != "sql.NullTime" {
		t.Errorf("deleted_at GoType = %v, want sql.NullTime", delCol)
	}
	// connection has no UNIQUE columns
	if len(conn.UniqueColumns) != 0 {
		t.Errorf("connection UniqueColumns = %v, want empty", conn.UniqueColumns)
	}

	// --- setting ---
	setting := tables[1]
	if len(setting.UniqueColumns) != 1 || setting.UniqueColumns[0].Name != "key" {
		t.Errorf("setting UniqueColumns = %+v, want [{key}]", setting.UniqueColumns)
	}

	// --- membership: composite PK, FK clauses ignored ---
	memb := tables[2]
	if len(memb.PKGoFields) != 2 {
		t.Errorf("membership PK fields = %v, want 2", memb.PKGoFields)
	}
	if memb.PKGoFields[0] != "UserID" || memb.PKGoFields[1] != "RoleID" {
		t.Errorf("membership PK go fields = %v, want [UserID RoleID]", memb.PKGoFields)
	}
	// Should have exactly 2 columns — FOREIGN KEY clauses must not become columns.
	if len(memb.Columns) != 2 {
		t.Errorf("membership columns = %d, want 2", len(memb.Columns))
	}
}

func TestParseSQLSchema_Postgres(t *testing.T) {
	path := writeTempSchema(t, `
CREATE TABLE IF NOT EXISTS event (
    id          BIGSERIAL PRIMARY KEY,
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    archived_at TIMESTAMPTZ
);
`)
	tables, err := parseSQLSchema(path, dialect.MustNew("postgres"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(tables))
	}
	ev := tables[0]
	if ev.PKGoType != "int64" {
		t.Errorf("PK type = %q, want int64 (BIGSERIAL)", ev.PKGoType)
	}
	var payload, created, archived *sqlColumn
	for i := range ev.Columns {
		switch ev.Columns[i].Name {
		case "payload":
			payload = &ev.Columns[i]
		case "created_at":
			created = &ev.Columns[i]
		case "archived_at":
			archived = &ev.Columns[i]
		}
	}
	if payload.GoType != "[]byte" {
		t.Errorf("payload (JSONB) GoType = %q, want []byte", payload.GoType)
	}
	if created.GoType != "time.Time" {
		t.Errorf("created_at (NOT NULL TIMESTAMPTZ) GoType = %q, want time.Time", created.GoType)
	}
	if archived.GoType != "sql.NullTime" {
		t.Errorf("archived_at (nullable TIMESTAMPTZ) GoType = %q, want sql.NullTime", archived.GoType)
	}
}

func TestParseSQLSchema_QuotedIdentifiers(t *testing.T) {
	// Reserved words like "group", "order", "user" must be quoted as table
	// names so the raw schema.sql works under sqlite/postgres. The parser
	// has to strip the wrapping quotes so generated Go field/struct names
	// don't end up as e.g. `"Group"`.
	path := writeTempSchema(t, `
CREATE TABLE IF NOT EXISTS "group" (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS `+"`order`"+` (
    id          INTEGER PRIMARY KEY,
    "total"     INTEGER NOT NULL DEFAULT 0,
    `+"`status`"+` TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS [user] (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL DEFAULT ''
);
`)
	tables, err := parseSQLSchema(path, dialect.MustNew("sqlite"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tables) != 3 {
		t.Fatalf("want 3 tables, got %d", len(tables))
	}

	want := map[string]string{"group": "Group", "order": "Order", "user": "User"}
	for _, tbl := range tables {
		exp, ok := want[tbl.Name]
		if !ok {
			t.Errorf("unexpected table %q", tbl.Name)
			continue
		}
		if tbl.GoName != exp {
			t.Errorf("table %q GoName = %q, want %q", tbl.Name, tbl.GoName, exp)
		}
	}

	// Column-level quoting (`status`, "total") must also be unwrapped.
	var orderTbl *sqlTable
	for i := range tables {
		if tables[i].Name == "order" {
			orderTbl = &tables[i]
		}
	}
	if orderTbl == nil {
		t.Fatal("order table not parsed")
	}
	wantCols := map[string]string{"total": "Total", "status": "Status"}
	for sqlName, goName := range wantCols {
		found := false
		for _, c := range orderTbl.Columns {
			if c.Name == sqlName {
				found = true
				if c.GoName != goName {
					t.Errorf("col %q GoName = %q, want %q", sqlName, c.GoName, goName)
				}
				break
			}
		}
		if !found {
			t.Errorf("order missing column %q", sqlName)
		}
	}
}

func TestParseSQLSchema_FKsIgnored(t *testing.T) {
	// Inline FK and table-level FK must not affect generated struct/columns.
	path := writeTempSchema(t, `
CREATE TABLE IF NOT EXISTS post (
    id      INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES "user"(id) ON DELETE CASCADE
);
`)
	tables, err := parseSQLSchema(path, dialect.MustNew("sqlite"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tables) != 1 || len(tables[0].Columns) != 2 {
		t.Fatalf("want 1 table with 2 cols, got %+v", tables)
	}
}
