package data

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tradalab/scorix/observe"
)

var ctx = context.Background()

var pool = Options{MaxOpen: 1, MaxIdle: 1, MaxLifetime: time.Minute}

func open(t *testing.T, opt Options) *DB {
	t.Helper()
	db, err := Open(ctx, "sqlite", ":memory:", opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE links (id INTEGER PRIMARY KEY, slug TEXT NOT NULL, title TEXT, views INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO links (id, slug, title, views) VALUES (1, 'a', 'First', 3), (2, 'b', NULL, 0)`,
	} {
		if _, err := db.ExecCtx(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

type audit struct {
	Views int64 `db:"views"`
}

type link struct {
	ID    int64          `db:"id"`
	Slug  string         `db:"slug"`
	Title sql.NullString `db:"title"`
	audit
	Note  string
	Cache string `db:"-"`
}

const cols = `SELECT id, slug, title, views FROM links`

func TestOpenRefusesAPoolItWasNotToldTheSizeOf(t *testing.T) {
	for name, opt := range map[string]Options{
		"Options.MaxOpen is required":     {MaxIdle: 1, MaxLifetime: time.Minute},
		"Options.MaxIdle must be":         {MaxOpen: 1, MaxIdle: 2, MaxLifetime: time.Minute},
		"Options.MaxLifetime is required": {MaxOpen: 1, MaxIdle: 1},
	} {
		if _, err := Open(ctx, "sqlite", ":memory:", opt); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("without %s: %v", name, err)
		}
	}
	if _, err := Open(ctx, "nodriver", "x", pool); err == nil {
		t.Error("an unknown driver opened")
	}
	if _, err := Open(ctx, "sqlite", "file:/nonexistent/dir/x.db?mode=ro", pool); err == nil || !strings.Contains(err.Error(), "data: sqlite:") {
		t.Errorf("a database that cannot be reached opened: %v", err)
	}
}

func TestThePoolIsTheSizeAsked(t *testing.T) {
	db := open(t, Options{MaxOpen: 3, MaxIdle: 2, MaxLifetime: time.Minute})
	if got := db.db.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("MaxOpenConnections = %d", got)
	}
}

func TestARowScansIntoTaggedFieldsIncludingEmbeddedOnes(t *testing.T) {
	db := open(t, pool)
	var l link
	if err := db.QueryRowCtx(ctx, &l, cols+` WHERE id = ?`, 1); err != nil {
		t.Fatal(err)
	}
	if l.ID != 1 || l.Slug != "a" || l.Title.String != "First" || l.Views != 3 {
		t.Fatalf("scanned %+v", l)
	}
	var n int64
	if err := db.QueryRowCtx(ctx, &n, `SELECT count(*) FROM links`); err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}
}

func TestNoRowIsErrNotFound(t *testing.T) {
	var l link
	if err := open(t, pool).QueryRowCtx(ctx, &l, cols+` WHERE id = 99`); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestQueryRowRefusesAQueryThatMatchesTwoRows(t *testing.T) {
	var l link
	if err := open(t, pool).QueryRowCtx(ctx, &l, cols); err == nil || !strings.Contains(err.Error(), "more than one row") {
		t.Fatalf("err = %v, want the second row refused, not dropped", err)
	}
}

func TestAColumnMismatchFailsEvenWhenNoRowMatches(t *testing.T) {
	db := open(t, pool)
	var l link
	if err := db.QueryRowCtx(ctx, &l, `SELECT id, slug, title FROM links WHERE 1 = 0`); err == nil || !strings.Contains(err.Error(), "no column for data.link field views") {
		t.Errorf("missing column: %v", err)
	}
	var ls []link
	if err := db.QueryRowsCtx(ctx, &ls, `SELECT id, slug, title, views, 1 AS extra FROM links WHERE 1 = 0`); err == nil || !strings.Contains(err.Error(), "no db field for column extra") {
		t.Errorf("extra column: %v", err)
	}
	var n int64
	if err := db.QueryRowCtx(ctx, &n, `SELECT id, slug FROM links WHERE 1 = 0`); err == nil || !strings.Contains(err.Error(), "returns 2 columns into one int64") {
		t.Errorf("two columns into a scalar: %v", err)
	}
}

func TestRowsScanIntoValuesPointersAndScalars(t *testing.T) {
	db := open(t, pool)
	var vals []link
	var ptrs []*link
	var slugs []string
	for dst, q := range map[any]string{&vals: cols + ` ORDER BY id`, &ptrs: cols + ` ORDER BY id`, &slugs: `SELECT slug FROM links ORDER BY id`} {
		if err := db.QueryRowsCtx(ctx, dst, q); err != nil {
			t.Fatal(err)
		}
	}
	if len(vals) != 2 || vals[1].Title.Valid || len(ptrs) != 2 || ptrs[0].Slug != "a" || strings.Join(slugs, ",") != "a,b" {
		t.Fatalf("vals %+v ptrs %+v slugs %v", vals, ptrs, slugs)
	}
	none := []link{{ID: 9}}
	if err := db.QueryRowsCtx(ctx, &none, cols+` WHERE 1 = 0`); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("an empty result left %v, %v", none, err)
	}
}

func TestADestinationThatCannotHoldRowsIsRefused(t *testing.T) {
	db := open(t, pool)
	var l link
	var nilLink *link
	for name, call := range map[string]func() error{
		"not a pointer": func() error { return db.QueryRowCtx(ctx, l, cols) },
		"nil pointer":   func() error { return db.QueryRowCtx(ctx, nilLink, cols) },
		"rows into one": func() error { return db.QueryRowsCtx(ctx, &l, cols) },
		"unexported tag": func() error {
			var b struct {
				x int `db:"id"`
			}
			return db.QueryRowCtx(ctx, &b, `SELECT id FROM links WHERE id = 1`)
		},
		"duplicate tag": func() error {
			var b struct {
				A int `db:"id"`
				B int `db:"id"`
			}
			return db.QueryRowCtx(ctx, &b, `SELECT id FROM links WHERE id = 1`)
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestArgumentsAndStatementsReachTheDriverUntouched(t *testing.T) {
	db := open(t, pool)
	var missing *time.Time
	if _, err := db.ExecCtx(ctx, `INSERT INTO links (id, slug, title) VALUES (3, 'c', ?)`, missing); err != nil {
		t.Fatalf("a nil *time.Time argument: %v", err)
	}
	var title sql.NullString
	if err := db.QueryRowCtx(ctx, &title, `SELECT title FROM links WHERE id = 3 -- at 10:05, :05 is not a parameter`); err != nil || title.Valid {
		t.Fatalf("comment with :05: %v %+v", err, title)
	}
	var lit string
	if err := db.QueryRowCtx(ctx, &lit, `SELECT 'a\' || ? || 'b'`, "-"); err != nil || lit != `a\-b` {
		t.Fatalf("backslash in a literal: %q %v", lit, err)
	}
}

func TestExecReportsWhatItTouched(t *testing.T) {
	res, err := open(t, pool).ExecCtx(ctx, `UPDATE links SET views = views + 1`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 2 {
		t.Fatalf("affected %d", n)
	}
}

func count(t *testing.T, db *DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowCtx(ctx, &n, `SELECT count(*) FROM links`); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestATransactionCommitsOnlyWhatReturnedNil(t *testing.T) {
	db := open(t, pool)
	insert := func(id int) func(context.Context, Session) error {
		return func(ctx context.Context, s Session) error {
			_, err := s.ExecCtx(ctx, `INSERT INTO links (id, slug) VALUES (?, 'x')`, id)
			return err
		}
	}
	if err := db.TransactCtx(ctx, insert(10)); err != nil || count(t, db) != 3 {
		t.Fatalf("commit: %v, %d rows", err, count(t, db))
	}
	refused := errors.New("refused")
	err := db.TransactCtx(ctx, func(ctx context.Context, s Session) error {
		if err := insert(11)(ctx, s); err != nil {
			return err
		}
		var inside int64
		if err := s.QueryRowCtx(ctx, &inside, `SELECT count(*) FROM links`); err != nil || inside != 4 {
			t.Errorf("inside the transaction: %d %v", inside, err)
		}
		return refused
	})
	if !errors.Is(err, refused) || count(t, db) != 3 {
		t.Fatalf("rollback: %v, %d rows", err, count(t, db))
	}
	func() {
		defer func() {
			if recover() != "boom" {
				t.Error("the panic was swallowed")
			}
		}()
		db.TransactCtx(ctx, func(ctx context.Context, s Session) error {
			insert(12)(ctx, s)
			panic("boom")
		})
	}()
	if count(t, db) != 3 {
		t.Fatalf("a panicking transaction committed: %d rows", count(t, db))
	}
}

func TestASlowStatementIsLoggedWithoutItsArguments(t *testing.T) {
	var buf bytes.Buffer
	log, _ := observe.NewLogger(&buf, "info")
	lctx := observe.WithLogger(ctx, log)
	quick := open(t, pool)
	var n int64
	quick.QueryRowCtx(lctx, &n, `SELECT count(*) FROM links WHERE slug = ?`, "hunter2")
	if buf.Len() != 0 {
		t.Fatalf("logged with Slow unset: %s", buf.String())
	}
	slow := open(t, Options{MaxOpen: 1, MaxIdle: 1, MaxLifetime: time.Minute, Slow: time.Nanosecond})
	slow.QueryRowCtx(lctx, &n, `SELECT count(*) FROM links WHERE slug = ?`, "hunter2")
	slow.ExecCtx(lctx, `UPDATE links SET views = ? WHERE slug = 'zz'`, 1)
	var ls []link
	slow.QueryRowsCtx(lctx, &ls, cols)
	out := buf.String()
	if strings.Count(out, `"msg":"slow sql"`) != 3 || !strings.Contains(out, "WHERE slug = ?") || strings.Contains(out, "hunter2") {
		t.Fatalf("log = %s", out)
	}
}
