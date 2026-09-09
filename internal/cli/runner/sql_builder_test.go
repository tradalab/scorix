package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
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
	// Update positional check - Postgres SET pos 1..N, WHERE pos N+1.
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

// The parser forces created_at/updated_at non-null so the Insert hook can call
// .IsZero(), but it cannot force the mapped type. A column whose SQL type the
// dialect does not read as a timestamp used to get the hook anyway, and the
// generated package would not compile.
func TestTimestampHooksNeedARealTimeColumn(t *testing.T) {
	cases := []struct {
		name    string
		sqlType string
		d       dialect.Dialect
		want    bool
	}{
		{"mapped to time.Time", "DATETIME", dialect.SQLite{}, true},
		{"declared TEXT", "TEXT", dialect.SQLite{}, false},
		{"DATETIME is not a postgres word", "DATETIME", dialect.Postgres{}, false},
		{"postgres spelling", "TIMESTAMPTZ", dialect.Postgres{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			schema := fmt.Sprintf(`CREATE TABLE thing (
  id TEXT PRIMARY KEY,
  created_at %[1]s,
  updated_at %[1]s
);`, c.sqlType)
			tables := parseInline(t, schema, c.d)
			got := buildSQL(tables[0], c.d)
			if got.HasCreatedAt != c.want || got.HasUpdatedAt != c.want {
				t.Errorf("created=%v updated=%v, want both %v (column mapped to %s)",
					got.HasCreatedAt, got.HasUpdatedAt, c.want, tables[0].Columns[1].GoType)
			}
		})
	}
}

// NeedsTimeImport drives the time import, so it has to mean "a field is
// time.Time". Keying it on the words DATETIME/TIMESTAMP made it true for a
// nullable column (sql.NullTime) and for a spelling the dialect does not know
// (string), and the generated package failed on an unused import.
func TestTimeImportFollowsTheMappedType(t *testing.T) {
	cases := []struct {
		name string
		col  string
		d    dialect.Dialect
		want bool
	}{
		{"nullable DATETIME becomes sql.NullTime", "seen_at DATETIME", dialect.SQLite{}, false},
		{"not-null DATETIME becomes time.Time", "seen_at DATETIME NOT NULL", dialect.SQLite{}, true},
		{"postgres does not know DATETIME", "seen_at DATETIME NOT NULL", dialect.Postgres{}, false},
		{"postgres spelling", "seen_at TIMESTAMPTZ NOT NULL", dialect.Postgres{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tables := parseInline(t, fmt.Sprintf(`CREATE TABLE t (
  id TEXT PRIMARY KEY,
  %s
);`, c.col), c.d)
			if got := buildSQL(tables[0], c.d).NeedsTimeImport; got != c.want {
				t.Errorf("NeedsTimeImport = %v, want %v (column mapped to %s)", got, c.want, tables[0].Columns[1].GoType)
			}
		})
	}
}

// The soft-delete branch stamps time.Now() as well, so a table whose only
// timestamp is deleted_at still needs the import. Nothing said so: it was
// covered by accident, by a HasTime that keyed on the word DATETIME.
func TestTimeImportCoversSoftDelete(t *testing.T) {
	tables := parseInline(t, `CREATE TABLE t (
  id TEXT PRIMARY KEY,
  deleted_at DATETIME
);`, dialect.SQLite{})
	s := buildSQL(tables[0], dialect.SQLite{})
	if !s.DeleteIsSoft {
		t.Fatal("deleted_at did not produce a soft delete, so this test proves nothing")
	}
	if !s.NeedsTimeImport {
		t.Error("Delete emits time.Now() but the import was not requested")
	}
}
