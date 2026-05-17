package sqlx

import (
	"context"
	"testing"
	"testing/fstest"
)

// fstest.MapFS lets us hand-craft a migrations directory in-memory without
// touching the disk. Each entry uses the goose naming convention.
func TestRunGooseUp_Idempotent(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_init.sql": &fstest.MapFile{
			Data: []byte(`-- +goose Up
CREATE TABLE acct (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL DEFAULT 0);
-- +goose Down
DROP TABLE acct;
`),
		},
		"migrations/00002_add_idx.sql": &fstest.MapFile{
			Data: []byte(`-- +goose Up
CREATE INDEX acct_balance_idx ON acct(balance);
-- +goose Down
DROP INDEX acct_balance_idx;
`),
		},
	}

	withInMemoryDB(t, func(m *Module) {
		ctx := context.Background()

		// First run — applies both migrations.
		if err := runGooseUp(ctx, m.db.DB, "sqlite3", fsys, "migrations"); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rows, _ := m.Query(ctx, SQLRequest{SQL: "SELECT COUNT(*) AS n FROM acct"})
		if rows[0]["n"].(int64) != 0 {
			t.Errorf("acct should be empty after migration, got %v", rows[0]["n"])
		}

		// Second run — idempotent, no error, version unchanged.
		if err := runGooseUp(ctx, m.db.DB, "sqlite3", fsys, "migrations"); err != nil {
			t.Fatalf("second up should be no-op: %v", err)
		}

		// Insert + Update across both migrations' artefacts works.
		if _, err := m.Exec(ctx, SQLRequest{SQL: "INSERT INTO acct (id, balance) VALUES (?, ?)", Args: []any{1, 50}}); err != nil {
			t.Fatal(err)
		}
		rows, _ = m.Query(ctx, SQLRequest{SQL: "SELECT balance FROM acct WHERE id = ?", Args: []any{1}})
		if rows[0]["balance"].(int64) != 50 {
			t.Errorf("expected 50, got %v", rows[0]["balance"])
		}
	})
}

func TestRunGooseUp_UnknownDriver(t *testing.T) {
	withInMemoryDB(t, func(m *Module) {
		fsys := fstest.MapFS{
			"migrations/00001_init.sql": &fstest.MapFile{
				Data: []byte(`-- +goose Up
CREATE TABLE x (id INTEGER);
`),
			},
		}
		err := runGooseUp(context.Background(), m.db.DB, "oracledb", fsys, "migrations")
		if err == nil {
			t.Fatal("expected error for unknown driver")
		}
	})
}

func TestWithMigrationsOption(t *testing.T) {
	// Nil fsys → no-op (don't panic, don't register a script).
	m := New(WithMigrations(nil, "migrations"))
	if len(m.initScripts) != 0 {
		t.Errorf("nil fsys should not register an init script, got %d", len(m.initScripts))
	}

	// Non-nil → script registered.
	fsys := fstest.MapFS{}
	m = New(WithMigrations(fsys, "migrations"))
	if len(m.initScripts) != 1 {
		t.Fatalf("want 1 init script, got %d", len(m.initScripts))
	}
}
