package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradalab/scorix/cli/runner/dialect"
)

// parseInline writes the SQL to a temp file under t.TempDir() and parses it.
func parseInline(t *testing.T, sql string, d dialect.Dialect) []sqlTable {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	tables, err := parseSQLSchema(path, d)
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

const connectionSchema = `
CREATE TABLE IF NOT EXISTS connection (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)))),
    name        TEXT NOT NULL DEFAULT '',
    port        INTEGER NOT NULL DEFAULT 6379,
    ssh_id      TEXT,
    created_at  DATETIME,
    updated_at  DATETIME,
    deleted_at  DATETIME
);
`

func TestBuildSQL_SQLite(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tbl := parseInline(t, connectionSchema, d)[0]
	sql := buildSQL(tbl, d)

	// Soft-delete must appear in FindOne/FindAll/FindMany (deleted_at present).
	for name, q := range map[string]string{
		"FindOne":  sql.FindOneSQL,
		"FindAll":  sql.FindAllSQL,
		"FindMany": sql.FindManyBaseSQL,
	} {
		if !strings.Contains(q, "`deleted_at` IS NULL") {
			t.Errorf("%s missing soft-delete clause: %s", name, q)
		}
	}

	// SQLite uses `?` placeholder.
	if !strings.Contains(sql.FindOneSQL, "= ?") {
		t.Errorf("FindOneSQL should use ? placeholder: %s", sql.FindOneSQL)
	}

	// Delete is soft (UPDATE), not hard.
	if !sql.DeleteIsSoft {
		t.Error("expected DeleteIsSoft for table with deleted_at")
	}
	if !strings.HasPrefix(sql.DeleteSQL, "UPDATE") {
		t.Errorf("DeleteSQL should start with UPDATE for soft delete: %s", sql.DeleteSQL)
	}

	// UUID hook triggered for PK string + DEFAULT containing randomblob.
	if !sql.NeedsUUIDHook {
		t.Error("expected NeedsUUIDHook for randomblob-based PK default")
	}

	// HasUpdatedAt / HasCreatedAt observed.
	if !sql.HasCreatedAt || !sql.HasUpdatedAt {
		t.Errorf("expected HasCreatedAt/HasUpdatedAt true, got %v / %v", sql.HasCreatedAt, sql.HasUpdatedAt)
	}

	// Update SET clause must skip PK and created_at, keep updated_at.
	if strings.Contains(sql.UpdateSQL, "`id` = ?") &&
		strings.Index(sql.UpdateSQL, "`id` = ?") < strings.Index(sql.UpdateSQL, "WHERE") {
		t.Errorf("UpdateSQL must not set PK in SET clause: %s", sql.UpdateSQL)
	}
	if strings.Contains(sql.UpdateSQL, "`created_at` = ?") {
		t.Errorf("UpdateSQL must not set created_at: %s", sql.UpdateSQL)
	}
	if !strings.Contains(sql.UpdateSQL, "`updated_at` = ?") {
		t.Errorf("UpdateSQL must set updated_at: %s", sql.UpdateSQL)
	}
}

func TestBuildSQL_Postgres(t *testing.T) {
	d := dialect.MustNew("postgres")
	tbl := parseInline(t, connectionSchema, d)[0]
	sql := buildSQL(tbl, d)

	// Postgres uses $N positional placeholders.
	if !strings.Contains(sql.FindOneSQL, "= $1") {
		t.Errorf("FindOneSQL should use $1 placeholder: %s", sql.FindOneSQL)
	}
	// FindMany keeps `?` for sqlx.In to expand, regardless of dialect.
	if !strings.Contains(sql.FindManyBaseSQL, "IN (?)") {
		t.Errorf("FindManyBaseSQL should keep IN (?) for sqlx.In: %s", sql.FindManyBaseSQL)
	}
	// Postgres quotes with double-quotes.
	if !strings.Contains(sql.FindOneSQL, `"id"`) {
		t.Errorf("FindOneSQL should double-quote identifiers: %s", sql.FindOneSQL)
	}
	// Update positional check — Postgres SET pos 1..N, WHERE pos N+1.
	if !strings.Contains(sql.UpdateSQL, "WHERE \"id\" = $") {
		t.Errorf("UpdateSQL postgres WHERE positional missing: %s", sql.UpdateSQL)
	}
}

func TestBuildSQL_MySQL_HardDelete(t *testing.T) {
	d := dialect.MustNew("mysql")
	// Table without deleted_at → hard DELETE.
	tables := parseInline(t, `
CREATE TABLE IF NOT EXISTS account (
    id     INTEGER PRIMARY KEY,
    email  TEXT NOT NULL UNIQUE
);
`, d)
	sql := buildSQL(tables[0], d)

	if sql.DeleteIsSoft {
		t.Error("expected hard delete when deleted_at absent")
	}
	if !strings.HasPrefix(sql.DeleteSQL, "DELETE FROM") {
		t.Errorf("DeleteSQL should start with DELETE FROM: %s", sql.DeleteSQL)
	}
	// FindOneByEmail emitted from UNIQUE column.
	if len(sql.FindOneByCols) != 1 || sql.FindOneByCols[0].GoName != "Email" {
		t.Errorf("expected FindOneByEmail, got %+v", sql.FindOneByCols)
	}
}
