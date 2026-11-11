package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	xsqlx "github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	"github.com/tradalab/scorix/data"
)

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

		if err := runMigrations(ctx, m.db.DB, "sqlite3", fsys, "migrations"); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rows, _ := m.Query(ctx, SQLRequest{SQL: "SELECT COUNT(*) AS n FROM acct"})
		if rows[0]["n"].(int64) != 0 {
			t.Errorf("acct should be empty after migration, got %v", rows[0]["n"])
		}

		// Second run must be idempotent: no error, version unchanged.
		if err := runMigrations(ctx, m.db.DB, "sqlite3", fsys, "migrations"); err != nil {
			t.Fatalf("second up should be no-op: %v", err)
		}

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
		err := runMigrations(context.Background(), m.db.DB, "oracledb", fsys, "migrations")
		if err == nil {
			t.Fatal("expected error for unknown driver")
		}
	})
}

func TestWithMigrationsOption(t *testing.T) {
	// Nil fsys -> no-op (don't panic, don't register a script).
	m := New(WithMigrations(nil, "migrations"))
	if len(m.initScripts) != 0 {
		t.Errorf("nil fsys should not register an init script, got %d", len(m.initScripts))
	}

	fsys := fstest.MapFS{}
	m = New(WithMigrations(fsys, "migrations"))
	if len(m.initScripts) != 1 {
		t.Fatalf("want 1 init script, got %d", len(m.initScripts))
	}
}

func TestWithMigrationsRunsRegisteredGoMigrationsBacksUpAndRefusesANewerDatabase(t *testing.T) {
	t.Cleanup(goose.ResetGlobalMigrations)
	noop := func(context.Context, *sql.Tx) error { return nil }
	goose.AddNamedMigrationContext("00002_columns.go", func(ctx context.Context, tx *sql.Tx) error {
		return AddColumn(ctx, tx, "acct", "note", "TEXT NOT NULL DEFAULT ''")
	}, noop)
	path := filepath.Join(t.TempDir(), "app.dat")
	db, err := xsqlx.Connect("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fsys := fstest.MapFS{"m/00001_init.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE acct (id INTEGER PRIMARY KEY);\n")}}
	up := func() error {
		m := New(WithMigrations(fsys, "m"))
		m.cfg.Driver = "sqlite"
		return m.initScripts[0](context.Background(), db)
	}

	if err := up(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Get(&n, `SELECT count(*) FROM pragma_table_info('acct') WHERE name = 'note'`); err != nil || n != 1 {
		t.Fatalf("the registered Go migration did not run: %d, %v", n, err)
	}
	fsys["m/00003_more.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")}
	if err := up(); err != nil {
		t.Fatal(err)
	}
	if copies, _ := filepath.Glob(path + ".v2.*.bak"); len(copies) != 1 {
		t.Fatalf("copies before the upgrade from v2 = %v", copies)
	}
	delete(fsys, "m/00003_more.sql")
	if err := up(); !errors.Is(err, data.ErrNewerDatabase) {
		t.Fatalf("an older build on a v3 database = %v", err)
	}
}

func TestOnlySQLiteRefusesANewerDatabaseAndKeepsCopies(t *testing.T) {
	for driver, sqlite := range map[string]bool{"sqlite": true, "sqlite3": true, "pgx": false, "postgres": false, "mysql": false} {
		m, err := migrations(driver, fstest.MapFS{}, "m")
		if err != nil || !m.Registered || m.RefuseNewer != sqlite || (m.Backups > 0) != sqlite {
			t.Errorf("%s = %+v, %v", driver, m, err)
		}
	}
	if _, err := migrations("oracledb", fstest.MapFS{}, "m"); err == nil {
		t.Error("an unknown driver was given a dialect")
	}
}
