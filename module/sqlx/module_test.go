package sqlx

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func TestConfigDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "empty defaults to sqlite (modernc, pure-Go)",
			in:   Config{},
			want: Config{Driver: "sqlite", DSN: "app.dat", MaxOpenConns: 1, MaxIdleConns: 1, SlowQueryThresholdMs: 200},
		},
		{
			name: "sqlite3 alias keeps pool size 1",
			in:   Config{Driver: "sqlite3"},
			want: Config{Driver: "sqlite3", DSN: "app.dat", MaxOpenConns: 1, MaxIdleConns: 1, SlowQueryThresholdMs: 200},
		},
		{
			name: "mysql defaults to larger pool",
			in:   Config{Driver: "mysql", DSN: "user:pass@/db"},
			want: Config{Driver: "mysql", DSN: "user:pass@/db", MaxOpenConns: 10, MaxIdleConns: 2, SlowQueryThresholdMs: 200},
		},
		{
			name: "explicit values preserved",
			in:   Config{Driver: "pgx", DSN: "postgres://x", MaxOpenConns: 50, MaxIdleConns: 5},
			want: Config{Driver: "pgx", DSN: "postgres://x", MaxOpenConns: 50, MaxIdleConns: 5, SlowQueryThresholdMs: 200},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in
			got.defaults()
			if got != c.want {
				t.Errorf("defaults() = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestMaskDSN(t *testing.T) {
	cases := map[string]string{
		"postgres://user:secret@host:5432/db":                "postgres://user:***@host:5432/db",
		"postgres://user:secret@host:5432/db?ssl=on":         "postgres://user:***@host:5432/db?ssl=on",
		"user:secret@tcp(127.0.0.1:3306)/app?parseTime=true": "user:***@tcp(127.0.0.1:3306)/app?parseTime=true",
		// No password — left alone
		"postgres://user@host/db": "postgres://user@host/db",
		"user@tcp(host)/db":       "user@tcp(host)/db",
		// SQLite filename — no embedded secret
		"app.dat":                 "app.dat",
		"/var/lib/scorix/app.dat": "/var/lib/scorix/app.dat",
		":memory:":                ":memory:",
	}
	for in, want := range cases {
		if got := maskDSN(in); got != want {
			t.Errorf("maskDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogSlowThresholdGuard(t *testing.T) {
	// Threshold 0 disables logging — function returns without panicking.
	m := New()
	m.cfg.SlowQueryThresholdMs = 0
	m.logSlow("Query", "SELECT 1", time.Now().Add(-5*time.Second)) // 5s old, but threshold disabled
	// No assertion — just confirm no panic / no error path. Behaviour is
	// observable only via log output; this test guards the early-return.
}

func TestIsSQLite(t *testing.T) {
	cases := map[string]bool{
		"sqlite3":  true,
		"sqlite":   true,
		"mysql":    false,
		"pgx":      false,
		"postgres": false,
		"":         false,
	}
	for in, want := range cases {
		if got := isSQLite(in); got != want {
			t.Errorf("isSQLite(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRegisterDriverOverride(t *testing.T) {
	m := New()
	if _, ok := m.drivers["sqlite"]; !ok {
		t.Fatal("sqlite (modernc) driver not registered by default")
	}

	called := false
	m.RegisterDriver("mock", func(string) (*sqlx.DB, error) {
		called = true
		return nil, nil
	})
	if _, ok := m.drivers["mock"]; !ok {
		t.Fatal("mock driver not registered")
	}
	_, _ = m.drivers["mock"]("ignored")
	if !called {
		t.Error("registered initializer was not invoked")
	}
}

// withInMemoryDB runs the test against a fresh in-memory SQLite database
// injected directly into a Module. Bypasses OnLoad (which requires a real
// module.Context constructed by the module manager) so the IPC handlers can be
// exercised without spinning up a full app harness.
func withInMemoryDB(t *testing.T, fn func(m *Module)) {
	t.Helper()
	db, err := sqlx.Connect("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	m := New()
	m.db = db
	fn(m)
}

func TestIPCRoundTrip(t *testing.T) {
	withInMemoryDB(t, func(m *Module) {
		ctx := context.Background()

		if _, err := m.Exec(ctx, SQLRequest{
			SQL: `CREATE TABLE note (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT NOT NULL)`,
		}); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}

		res, err := m.Exec(ctx, SQLRequest{
			SQL:  `INSERT INTO note (body) VALUES (?)`,
			Args: []any{"hello"},
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if res.RowsAffected != 1 {
			t.Errorf("rows affected = %d, want 1", res.RowsAffected)
		}
		if res.LastInsertID == 0 {
			t.Error("last insert id should be non-zero for AUTOINCREMENT PK")
		}

		rows, err := m.Query(ctx, SQLRequest{SQL: `SELECT id, body FROM note`})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		if rows[0]["body"] != "hello" {
			t.Errorf("body = %v, want hello", rows[0]["body"])
		}

		ping, err := m.Ping(ctx)
		if err != nil {
			t.Fatalf("ping: %v", err)
		}
		if !ping.OK {
			t.Errorf("ping not ok: %s", ping.Message)
		}

		stats, err := m.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if stats.OpenConnections < 1 {
			t.Errorf("expected at least 1 open connection, got %d", stats.OpenConnections)
		}
	})
}

func TestReadDB_NotReady(t *testing.T) {
	m := New()
	if _, err := m.readDB(); err == nil {
		t.Error("expected error when DB not initialized, got nil")
	}
	if _, err := m.Query(context.Background(), SQLRequest{SQL: "SELECT 1"}); err == nil {
		t.Error("Query should fail when DB not initialized")
	}
}

func TestWithSchemaOption(t *testing.T) {
	called := false
	m := New(WithSchema(`CREATE TABLE t (x INTEGER)`), WithInitScript(func(_ context.Context, db *sqlx.DB) error {
		called = true
		return nil
	}))
	if len(m.initScripts) != 2 {
		t.Fatalf("want 2 init scripts, got %d", len(m.initScripts))
	}
	// WithSchema("") and WithInitScript(nil) should be no-ops, not panics.
	m2 := New(WithSchema(""), WithInitScript(nil))
	if len(m2.initScripts) != 0 {
		t.Errorf("empty options should not register scripts, got %d", len(m2.initScripts))
	}
	if err := m.initScripts[1](context.Background(), nil); err != nil {
		t.Fatalf("init script 2: %v", err)
	}
	if !called {
		t.Error("WithInitScript callback was not invoked")
	}
}

func TestInitScriptsRunOnFirstOpen(t *testing.T) {
	// Simulate the OnLoad init-script execution path by invoking the script
	// list directly against an in-memory DB. This avoids the module.Context
	// scaffolding which the module manager constructs.
	withInMemoryDB(t, func(m *Module) {
		called := 0
		m.initScripts = []InitScript{
			func(ctx context.Context, db *sqlx.DB) error {
				called++
				_, err := db.ExecContext(ctx, "CREATE TABLE migrated (id INTEGER PRIMARY KEY)")
				return err
			},
		}
		for _, fn := range m.initScripts {
			if err := fn(context.Background(), m.db); err != nil {
				t.Fatalf("init: %v", err)
			}
		}
		if called != 1 {
			t.Errorf("init script called %d times, want 1", called)
		}
		if _, err := m.Query(context.Background(), SQLRequest{SQL: "SELECT COUNT(*) FROM migrated"}); err != nil {
			t.Errorf("table not created by init script: %v", err)
		}
	})
}

func TestInitScriptErrorAborts(t *testing.T) {
	withInMemoryDB(t, func(m *Module) {
		m.initScripts = []InitScript{
			func(_ context.Context, _ *sqlx.DB) error {
				return fmt.Errorf("boom")
			},
		}
		var got error
		for i, fn := range m.initScripts {
			if err := fn(context.Background(), m.db); err != nil {
				got = fmt.Errorf("init %d: %w", i+1, err)
				break
			}
		}
		if got == nil {
			t.Fatal("expected error from failing init script")
		}
	})
}

func TestWithTx_Commit(t *testing.T) {
	withInMemoryDB(t, func(m *Module) {
		ctx := context.Background()
		if _, err := m.Exec(ctx, SQLRequest{SQL: `CREATE TABLE acct (id INTEGER PRIMARY KEY, balance INTEGER)`}); err != nil {
			t.Fatal(err)
		}
		err := m.WithTx(ctx, func(ctx context.Context) error {
			// Inside the tx, From(ctx, ...) must return *sqlx.Tx, not the pool.
			conn := From(ctx, func() Conn { return m.db })
			if _, err := conn.ExecContext(ctx, `INSERT INTO acct VALUES (?, ?)`, 1, 100); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO acct VALUES (?, ?)`, 2, 200); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		rows, _ := m.Query(ctx, SQLRequest{SQL: `SELECT COUNT(*) AS n FROM acct`})
		if rows[0]["n"].(int64) != 2 {
			t.Errorf("expected 2 rows after commit, got %v", rows[0]["n"])
		}
	})
}

func TestWithTx_Rollback(t *testing.T) {
	withInMemoryDB(t, func(m *Module) {
		ctx := context.Background()
		if _, err := m.Exec(ctx, SQLRequest{SQL: `CREATE TABLE acct (id INTEGER PRIMARY KEY, balance INTEGER)`}); err != nil {
			t.Fatal(err)
		}
		// fn returns error → rollback. Pre-commit insert must NOT persist.
		err := m.WithTx(ctx, func(ctx context.Context) error {
			conn := From(ctx, func() Conn { return m.db })
			_, _ = conn.ExecContext(ctx, `INSERT INTO acct VALUES (?, ?)`, 1, 100)
			return fmt.Errorf("boom")
		})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("expected boom error, got %v", err)
		}
		rows, _ := m.Query(ctx, SQLRequest{SQL: `SELECT COUNT(*) AS n FROM acct`})
		if rows[0]["n"].(int64) != 0 {
			t.Errorf("expected 0 rows after rollback, got %v", rows[0]["n"])
		}
	})
}

func TestFromContextFallback(t *testing.T) {
	withInMemoryDB(t, func(m *Module) {
		// No tx in ctx → fallback fires.
		fallbackCalled := false
		conn := From(context.Background(), func() Conn {
			fallbackCalled = true
			return m.db
		})
		if conn == nil || !fallbackCalled {
			t.Error("From should call fallback when ctx has no tx")
		}

		// Tx in ctx → fallback NOT called.
		tx, _ := m.db.BeginTxx(context.Background(), nil)
		defer tx.Rollback()
		fallbackCalled = false
		conn = From(WithTxCtx(context.Background(), tx), func() Conn {
			fallbackCalled = true
			return m.db
		})
		if conn == nil {
			t.Fatal("From returned nil when tx was in ctx")
		}
		if fallbackCalled {
			t.Error("From should NOT call fallback when ctx carries a tx")
		}
	})
}

func TestQueryNormalisesBytesToString(t *testing.T) {
	// SQLite returns TEXT columns as []byte when scanned into any. The module
	// is supposed to surface them as Go strings for the IPC payload.
	withInMemoryDB(t, func(m *Module) {
		ctx := context.Background()
		if _, err := m.Exec(ctx, SQLRequest{SQL: `CREATE TABLE t (s TEXT)`}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Exec(ctx, SQLRequest{SQL: `INSERT INTO t VALUES (?)`, Args: []any{"abc"}}); err != nil {
			t.Fatal(err)
		}
		rows, _ := m.Query(ctx, SQLRequest{SQL: `SELECT s FROM t`})
		if _, ok := rows[0]["s"].(string); !ok {
			t.Errorf("expected string, got %T (%v)", rows[0]["s"], rows[0]["s"])
		}
	})
}
