package order

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

var spec = Spec{Table: "conn", OrderColumn: "sort_order", GroupColumn: "group_id", DeletedColumn: "deleted_at", UpdatedColumn: "updated_at"}

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db := sqlx.MustOpen("sqlite", filepath.Join(t.TempDir(), "t.db"))
	t.Cleanup(func() { db.Close() })
	db.MustExec(`CREATE TABLE conn (id TEXT PRIMARY KEY, group_id TEXT NOT NULL DEFAULT '', sort_order INTEGER NOT NULL DEFAULT 0, tags TEXT NOT NULL DEFAULT '[]', updated_at DATETIME, deleted_at DATETIME)`)
	return db
}

func rows(t *testing.T, db *sqlx.DB) []string {
	t.Helper()
	var out []string
	if err := db.Select(&out, `SELECT id || ':' || group_id || ':' || sort_order FROM conn ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNextIsZeroForAnEmptyGroupAndSkipsDeletedRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if n, err := Next(ctx, db, spec, "g"); err != nil || n != 0 {
		t.Fatalf("empty group: %d, %v", n, err)
	}
	db.MustExec(`INSERT INTO conn (id, group_id, sort_order) VALUES ('a','g',0), ('b','g',4), ('c','h',9)`)
	db.MustExec(`INSERT INTO conn (id, group_id, sort_order, deleted_at) VALUES ('x','g',50, CURRENT_TIMESTAMP)`)
	if n, _ := Next(ctx, db, spec, "g"); n != 5 {
		t.Fatalf("next in g = %d, want 5 (the deleted row at 50 does not count)", n)
	}
}

func TestReorderMovesIntoTheGroupNumbersInOrderAndLeavesOthersAlone(t *testing.T) {
	db := testDB(t)
	db.MustExec(`INSERT INTO conn (id, group_id, sort_order) VALUES ('a','',0), ('b','g',0), ('c','g',1), ('d','h',7)`)
	db.MustExec(`INSERT INTO conn (id, group_id, sort_order, deleted_at) VALUES ('x','',3, CURRENT_TIMESTAMP)`)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := Reorder(context.Background(), db, spec, "g", []string{"c", "a", "c", "x", "b", "ghost"}, now); err != nil {
		t.Fatal(err)
	}
	want := []string{"a:g:1", "b:g:3", "c:g:0", "d:h:7", "x::3"}
	if got := rows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v (repeat keeps first place; a deleted or unknown id leaves a harmless gap)", got, want)
	}
	var stamped int
	_ = db.Get(&stamped, `SELECT COUNT(*) FROM conn WHERE updated_at IS NOT NULL`)
	if stamped != 3 {
		t.Fatalf("%d rows stamped, want the 3 live ones that moved", stamped)
	}
}

func TestReorderWithNoIdsDoesNothing(t *testing.T) {
	db := testDB(t)
	db.MustExec(`INSERT INTO conn (id, group_id, sort_order) VALUES ('a','g',5)`)
	if err := Reorder(context.Background(), db, spec, "h", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := rows(t, db); !reflect.DeepEqual(got, []string{"a:g:5"}) {
		t.Fatalf("rows = %v", got)
	}
}

func TestASpecWithoutGroupOrSoftDeleteOrdersTheWholeTable(t *testing.T) {
	db := testDB(t)
	db.MustExec(`INSERT INTO conn (id) VALUES ('a'), ('b')`)
	plain := Spec{Table: "conn", OrderColumn: "sort_order"}
	if err := Reorder(context.Background(), db, plain, "ignored", []string{"b", "a"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := Next(context.Background(), db, plain, ""); n != 2 {
		t.Fatalf("next = %d", n)
	}
	if got := rows(t, db); !reflect.DeepEqual(got, []string{"a::1", "b::0"}) {
		t.Fatalf("rows = %v", got)
	}
}

func TestIdentifiersThatCouldInjectAreRefused(t *testing.T) {
	db := testDB(t)
	for _, s := range []Spec{
		{Table: `conn"; DROP TABLE conn; --`, OrderColumn: "sort_order"},
		{Table: "conn", OrderColumn: "sort order"},
		{Table: "conn"},
	} {
		if _, err := Next(context.Background(), db, s, ""); err == nil {
			t.Fatalf("%+v accepted", s)
		}
	}
}

func TestTagsRoundTripThroughTheDatabaseNormalized(t *testing.T) {
	db := testDB(t)
	db.MustExec(`INSERT INTO conn (id) VALUES ('a')`)
	if _, err := db.Exec(`UPDATE conn SET tags = ? WHERE id = 'a'`, Tags{" eu ", "EU", "", "kafka"}); err != nil {
		t.Fatal(err)
	}
	var got Tags
	if err := db.Get(&got, `SELECT tags FROM conn WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Tags{"eu", "kafka"}) {
		t.Fatalf("tags = %#v", got)
	}
	var raw string
	_ = db.Get(&raw, `SELECT tags FROM conn WHERE id = 'a'`)
	if raw != `["eu","kafka"]` {
		t.Fatalf("stored %q", raw)
	}
}

func TestTagsReadBadOrNullValuesAsEmpty(t *testing.T) {
	for _, src := range []any{nil, "", "null", "{", `[1,2]`, []byte(`["a","A"]`)} {
		var tg Tags
		if err := tg.Scan(src); err != nil {
			t.Fatalf("%v: %v", src, err)
		}
		if tg == nil {
			t.Fatalf("%v: nil tags would encode as null", src)
		}
	}
	var tg Tags
	if err := tg.Scan(42); err == nil {
		t.Fatal("an int column was accepted")
	}
	if v, _ := (Tags(nil)).Value(); v != "[]" {
		t.Fatalf("empty tags stored as %v", v)
	}
}
