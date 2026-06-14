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
	if len(conn.UniqueColumns) != 0 {
		t.Errorf("connection UniqueColumns = %v, want empty", conn.UniqueColumns)
	}

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
	// Reserved words ("group", "order", "user") are quoted in schema.sql; the
	// parser must strip the wrapping quotes so Go names aren't e.g. `"Group"`.
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

// Regression: CREATE TABLE inside a string literal (sqlite_master migration) must not become a real table.
func TestParseSQLSchema_IgnoresCreateTableInsideStringLiteral(t *testing.T) {
	path := writeTempSchema(t, `
CREATE TABLE IF NOT EXISTS chats (
    id      TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
    title   TEXT NOT NULL DEFAULT 'New Chat',
    pinned  INTEGER NOT NULL DEFAULT 0
);

PRAGMA writable_schema = ON;
UPDATE sqlite_master
SET sql = 'CREATE TABLE chats (
    id      TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
    title   TEXT NOT NULL DEFAULT ''New Chat'',
    pinned  INTEGER NOT NULL DEFAULT 0
)'
WHERE type = 'table' AND name = 'chats' AND sql NOT LIKE '%pinned%';
PRAGMA writable_schema = RESET;

CREATE INDEX IF NOT EXISTS idx_chats_pinned ON chats(pinned DESC);

CREATE TABLE IF NOT EXISTS messages (
    id      TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL
);
`)
	tables, err := parseSQLSchema(path, dialect.MustNew("sqlite"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("want 2 tables (chats + messages), got %d: %+v", len(tables), tables)
	}
	if tables[0].Name != "chats" {
		t.Errorf("first table = %q, want chats", tables[0].Name)
	}
	if tables[1].Name != "messages" {
		t.Errorf("second table = %q, want messages", tables[1].Name)
	}
	for _, c := range tables[0].Columns {
		switch c.Name {
		case "WHERE", "PRAGMA", "UPDATE", "CREATE":
			t.Errorf("chats has spurious column %q from migration text bleed", c.Name)
		}
	}
	if len(tables[0].Columns) != 3 {
		t.Errorf("chats cols = %d, want 3 (id, title, pinned); got %+v", len(tables[0].Columns), tables[0].Columns)
	}
}

// H-CG1 regression: the body splits on top-level commas, not newlines. A def may
// span lines or share a line, and commas nested in parens (DECIMAL(10,2),
// CHECK(... IN (1,2)), DEFAULT (json_array(...))) must not split a def.
func TestParseSQLSchema_TopLevelCommaSplit(t *testing.T) {
	type colWant struct {
		sqlType string
		goType  string
	}
	cases := []struct {
		name    string
		ddl     string
		wantCol map[string]colWant // sql column name → expected types
		// columns that must NOT exist (proves a def wasn't wrongly split)
		notCol []string
		// exact expected column count
		wantCount int
	}{
		{
			name: "multi-line single column definition",
			ddl: `
CREATE TABLE IF NOT EXISTS t (
    id    INTEGER PRIMARY KEY,
    meta  TEXT NOT NULL DEFAULT (
        json_object('a', 1, 'b', 2)
    ),
    name  TEXT NOT NULL
);`,
			wantCol: map[string]colWant{
				"id":   {"INTEGER", "int64"},
				"meta": {"TEXT", "string"},
				"name": {"TEXT", "string"},
			},
			notCol:    []string{"json_object", "a", "b"},
			wantCount: 3,
		},
		{
			name: "two columns on one physical line",
			ddl: `
CREATE TABLE IF NOT EXISTS t (
    id INTEGER PRIMARY KEY, name TEXT NOT NULL
);`,
			wantCol: map[string]colWant{
				"id":   {"INTEGER", "int64"},
				"name": {"TEXT", "string"},
			},
			wantCount: 2,
		},
		{
			name: "DECIMAL precision comma must not split",
			ddl: `
CREATE TABLE IF NOT EXISTS t (
    id    INTEGER PRIMARY KEY,
    price DECIMAL(10,2) NOT NULL DEFAULT 0,
    qty   INTEGER NOT NULL
);`,
			wantCol: map[string]colWant{
				"id":    {"INTEGER", "int64"},
				"price": {"DECIMAL", "float64"},
				"qty":   {"INTEGER", "int64"},
			},
			// a wrongly-split "2) NOT NULL DEFAULT 0" would create a bogus "2" col
			notCol:    []string{"2", "10"},
			wantCount: 3,
		},
		{
			name: "CHECK with nested IN list and inline CHECK constraint",
			ddl: `
CREATE TABLE IF NOT EXISTS t (
    id     INTEGER PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('a', 'b', 'c')),
    rank   INTEGER NOT NULL
);`,
			wantCol: map[string]colWant{
				"id":     {"INTEGER", "int64"},
				"status": {"TEXT", "string"},
				"rank":   {"INTEGER", "int64"},
			},
			notCol:    []string{"a", "b", "c", "status IN"},
			wantCount: 3,
		},
		{
			name: "DEFAULT containing a comma inside parens",
			ddl: `
CREATE TABLE IF NOT EXISTS t (
    id   TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)))),
    tags TEXT NOT NULL DEFAULT (json_array('x', 'y'))
);`,
			wantCol: map[string]colWant{
				"id":   {"TEXT", "string"},
				"tags": {"TEXT", "string"},
			},
			notCol:    []string{"x", "y", "json_array"},
			wantCount: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempSchema(t, tc.ddl)
			tables, err := parseSQLSchema(path, dialect.MustNew("sqlite"))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(tables) != 1 {
				t.Fatalf("want 1 table, got %d: %+v", len(tables), tables)
			}
			tbl := tables[0]
			if len(tbl.Columns) != tc.wantCount {
				t.Errorf("column count = %d, want %d; cols = %+v",
					len(tbl.Columns), tc.wantCount, tbl.Columns)
			}
			byName := map[string]sqlColumn{}
			for _, c := range tbl.Columns {
				byName[c.Name] = c
			}
			for name, want := range tc.wantCol {
				got, ok := byName[name]
				if !ok {
					t.Errorf("missing column %q (cols = %+v)", name, tbl.Columns)
					continue
				}
				if got.SQLType != want.sqlType {
					t.Errorf("col %q SQLType = %q, want %q", name, got.SQLType, want.sqlType)
				}
				if got.GoType != want.goType {
					t.Errorf("col %q GoType = %q, want %q", name, got.GoType, want.goType)
				}
			}
			for _, name := range tc.notCol {
				if _, ok := byName[name]; ok {
					t.Errorf("spurious column %q — a definition was wrongly split", name)
				}
			}
		})
	}
}

// Two real CREATE TABLE with the same name dedup (first wins).
func TestParseSQLSchema_DedupesDuplicateTableNames(t *testing.T) {
	path := writeTempSchema(t, `
CREATE TABLE IF NOT EXISTS foo (
    id TEXT PRIMARY KEY,
    a  TEXT
);

CREATE TABLE IF NOT EXISTS foo (
    id TEXT PRIMARY KEY,
    b  TEXT
);
`)
	tables, err := parseSQLSchema(path, dialect.MustNew("sqlite"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("want 1 deduped table, got %d", len(tables))
	}
	var hasA bool
	for _, c := range tables[0].Columns {
		if c.Name == "a" {
			hasA = true
		}
	}
	if !hasA {
		t.Error("first occurrence should win — column `a` missing")
	}
}
