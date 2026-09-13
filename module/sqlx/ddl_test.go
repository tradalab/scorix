package sqlx

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A file per test rather than one shared in-memory name: sql.DB is a pool, so a
// single connection left open would keep a shared-cache database alive and the
// next test would meet its own table again.
func ddlTx(t *testing.T) *sql.Tx {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ddl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func TestHasTableAndHasColumn(t *testing.T) {
	tx, ctx := ddlTx(t), context.Background()

	for _, c := range []struct {
		what string
		got  func() (bool, error)
		want bool
	}{
		{"table t", func() (bool, error) { return HasTable(ctx, tx, "t") }, true},
		{"table nope", func() (bool, error) { return HasTable(ctx, tx, "nope") }, false},
		{"t.id", func() (bool, error) { return HasColumn(ctx, tx, "t", "id") }, true},
		{"t.ghost", func() (bool, error) { return HasColumn(ctx, tx, "t", "ghost") }, false},
		// A missing table has no columns rather than an error, which is why
		// AddColumn cannot rely on HasColumn alone.
		{"nope.id", func() (bool, error) { return HasColumn(ctx, tx, "nope", "id") }, false},
	} {
		got, err := c.got()
		if err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.what, got, c.want)
		}
	}
}

// The whole point of the helper: one migration runs against a fresh install that
// already has the column and an older one that does not.
func TestAddColumnIsIdempotent(t *testing.T) {
	tx, ctx := ddlTx(t), context.Background()

	for i := range 2 {
		if err := AddColumn(ctx, tx, "t", "note", "TEXT NOT NULL DEFAULT ''"); err != nil {
			t.Fatalf("AddColumn call %d: %v", i+1, err)
		}
	}
	if has, err := HasColumn(ctx, tx, "t", "note"); err != nil || !has {
		t.Fatalf("note missing after AddColumn (err=%v)", err)
	}
}

// Skipped, not failed: this migration may run before the one that creates the
// table, and ALTER TABLE would abort the whole chain.
func TestAddColumnSkipsAMissingTable(t *testing.T) {
	if err := AddColumn(context.Background(), ddlTx(t), "nope", "c", "TEXT"); err != nil {
		t.Fatalf("AddColumn on a missing table: %v", err)
	}
}

func TestDropColumnOnlyWhenPresent(t *testing.T) {
	tx, ctx := ddlTx(t), context.Background()

	if err := AddColumn(ctx, tx, "t", "tmp", "TEXT"); err != nil {
		t.Fatal(err)
	}
	if err := DropColumn(ctx, tx, "t", "tmp"); err != nil {
		t.Fatalf("DropColumn: %v", err)
	}
	if has, err := HasColumn(ctx, tx, "t", "tmp"); err != nil || has {
		t.Fatalf("tmp still there (err=%v)", err)
	}
	// A second drop is a no-op, so re-running the migration does not fail.
	if err := DropColumn(ctx, tx, "t", "tmp"); err != nil {
		t.Fatalf("second DropColumn: %v", err)
	}
}
