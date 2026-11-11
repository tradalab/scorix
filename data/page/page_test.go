package page

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/tradalab/scorix/fault"
)

var byDayThenName = Spec{Default: 2, Max: 3, Order: []Order{{Column: "day", Desc: true}, {Column: "name"}, {Column: "id"}}}

type filter struct{ Owner string }

func db(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	rows := []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, owner TEXT, day TEXT, name TEXT)`,
		`INSERT INTO items VALUES
		 (7, 'ana', '2026-11-03', 'b'), (3, 'ana', '2026-11-03', 'b'), (5, 'ana', '2026-11-03', 'a'),
		 (1, 'ana', '2026-11-02', 'z'), (9, 'ana', '2026-11-01', 'c'), (2, 'ana', '2026-11-01', 'c'),
		 (4, 'ana', '2026-11-01', 'a'), (8, 'bob', '2026-11-03', 'a')`,
	}
	for _, q := range rows {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func fetch(t *testing.T, d *sql.DB, p Page, owner string) ([]int64, string) {
	t.Helper()
	q := "SELECT id, day, name FROM items WHERE owner = ?1 AND " + p.Boundary(2) + " ORDER BY " + p.OrderBy() + fmt.Sprintf(" LIMIT %d", p.Fetch())
	rows, err := d.QueryContext(context.Background(), q, append([]any{owner}, p.Args()...)...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	type row struct {
		id        int64
		day, name string
	}
	var got []row
	for rows.Next() {
		var r row
		rows.Scan(&r.id, &r.day, &r.name)
		got = append(got, r)
	}
	var last []any
	if len(got) > p.Size {
		r := got[p.Size-1]
		last = []any{r.day, r.name, r.id}
	}
	keep, next, err := p.Next(len(got), last...)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, keep)
	for i := range ids {
		ids[i] = got[i].id
	}
	return ids, next
}

func TestWalkingEveryPageSeesEachRowOnceInOrder(t *testing.T) {
	d := db(t)
	var seen []int64
	tok := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("the walk never ended")
		}
		p, err := byDayThenName.Start(2, tok, filter{"ana"})
		if err != nil {
			t.Fatal(err)
		}
		ids, next := fetch(t, d, p, "ana")
		seen = append(seen, ids...)
		if next == "" {
			break
		}
		tok = next
	}
	if got := fmt.Sprint(seen); got != "[5 3 7 1 4 2 9]" {
		t.Fatalf("walk = %s, want day desc, then name, then id", got)
	}
}

func TestTheLastPageHasNoTokenEvenWhenItIsFull(t *testing.T) {
	d := db(t)
	p, _ := byDayThenName.Start(1, "", filter{"bob"})
	if ids, next := fetch(t, d, p, "bob"); len(ids) != 1 || next != "" {
		t.Fatalf("ids %v next %q, want one row and no next page", ids, next)
	}
}

func TestPageSizeIsBoundedByTheServer(t *testing.T) {
	for asked, want := range map[int32]int{0: 2, 1: 1, 3: 3, 600: 3} {
		p, err := byDayThenName.Start(asked, "", nil)
		if err != nil || p.Size != want {
			t.Errorf("page_size %d = %d %v, want %d", asked, p.Size, err, want)
		}
	}
	if _, err := byDayThenName.Start(-1, "", nil); fault.CodeOf(err) != fault.CodeInvalid {
		t.Errorf("negative page_size: %v", err)
	}
}

func TestATokenBelongsToTheFilterAndSortItWasCutUnder(t *testing.T) {
	d := db(t)
	p, _ := byDayThenName.Start(2, "", filter{"ana"})
	_, next := fetch(t, d, p, "ana")
	if _, err := byDayThenName.Start(2, next, filter{"bob"}); fault.CodeOf(err) != fault.CodeInvalid || !strings.Contains(err.Error(), "another filter") {
		t.Errorf("other filter: %v", err)
	}
	resorted := byDayThenName
	resorted.Order = []Order{{Column: "day"}, {Column: "name"}, {Column: "id"}}
	if _, err := resorted.Start(2, next, filter{"ana"}); fault.CodeOf(err) != fault.CodeInvalid {
		t.Errorf("other sort: %v", err)
	}
	for _, bad := range []string{"!!!", "e30", "eyJmIjoieCIsImsiOltdfQ"} {
		if _, err := byDayThenName.Start(2, bad, filter{"ana"}); fault.CodeOf(err) != fault.CodeInvalid || !strings.Contains(err.Error(), "not one this list issued") {
			t.Errorf("token %q: %v", bad, err)
		}
	}
}

func TestTheFirstPageHasNoBoundaryAndPostgresPlaceholdersByDefault(t *testing.T) {
	spec := Spec{Default: 1, Max: 1, Order: []Order{{Column: "created_at", Desc: true}, {Column: "id", Desc: true}}}
	p, _ := spec.Start(0, "", nil)
	if p.Boundary(3) != "TRUE" || len(p.Args()) != 0 {
		t.Fatalf("first page: %q %v", p.Boundary(3), p.Args())
	}
	_, next, _ := p.Next(2, "2026-11-01T00:00:00Z", int64(9007199254740993))
	p, err := spec.Start(0, next, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "((created_at < $3) OR (created_at = $3 AND id < $4))"; p.Boundary(3) != want {
		t.Fatalf("boundary = %q, want %q", p.Boundary(3), want)
	}
	if args := p.Args(); fmt.Sprint(args) != "[2026-11-01T00:00:00Z 9007199254740993]" {
		t.Fatalf("args = %v, want the id without float rounding", args)
	}
	if _, _, err := p.Next(2, "only one"); err == nil {
		t.Fatal("Next took fewer sort values than the order has")
	}
}

func TestContainsTakesTheUsersWildcardsLiterally(t *testing.T) {
	d, _ := sql.Open("sqlite", ":memory:")
	defer d.Close()
	d.Exec(`CREATE TABLE t (s TEXT)`)
	d.Exec(`INSERT INTO t VALUES ('50% off'), ('500 off'), ('a_b'), ('axb'), ('back\slash')`)
	for q, want := range map[string]int{"50%": 1, "a_b": 1, `k\s`: 1, "off": 2} {
		var n int
		d.QueryRow(`SELECT count(*) FROM t WHERE s LIKE ? ESCAPE '\'`, Contains(q)).Scan(&n)
		if n != want {
			t.Errorf("Contains(%q) matched %d rows, want %d", q, n, want)
		}
	}
}

func TestTheTotalIsCountedOnlyWhenAskedAndOnlyOnTheFirstPage(t *testing.T) {
	first, _ := byDayThenName.Start(2, "", filter{"ana"})
	if !first.CountTotal(true) || first.CountTotal(false) {
		t.Fatal("first page: count only when asked")
	}
	_, next, _ := first.Next(3, "2026-11-03", "b", int64(3))
	later, _ := byDayThenName.Start(2, next, filter{"ana"})
	if later.CountTotal(true) {
		t.Fatal("a later page ran the count")
	}
}

func TestANullSortValueIsRefusedByItsColumn(t *testing.T) {
	p, _ := byDayThenName.Start(2, "", filter{"ana"})
	for _, null := range []any{nil, (*string)(nil)} {
		if _, _, err := p.Next(3, "2026-11-03", null, int64(3)); err == nil || !strings.Contains(err.Error(), "sort column name is NULL") {
			t.Errorf("Next with %#v for name: %v", null, err)
		}
	}
}

func TestATokenWhoseValuesAreNotScalarsIsRefused(t *testing.T) {
	p, _ := byDayThenName.Start(2, "", filter{"ana"})
	_, next, _ := p.Next(3, "2026-11-03", "b", int64(3))
	raw, _ := base64.RawURLEncoding.DecodeString(next)
	for _, forged := range []string{`{"a":1}`, `[1]`, `null`} {
		tok := base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(raw), `"b"`, forged, 1)))
		if _, err := byDayThenName.Start(2, tok, filter{"ana"}); fault.CodeOf(err) != fault.CodeInvalid {
			t.Errorf("a token value %s: %v, want invalid argument", forged, err)
		}
	}
	pinned := Spec{Default: 1, Max: 1, Order: []Order{{Column: "pinned", Desc: true}, {Column: "id"}}}
	first, _ := pinned.Start(0, "", nil)
	_, next, _ = first.Next(2, true, int64(4))
	if _, err := pinned.Start(0, next, nil); err != nil {
		t.Errorf("a token over a bool column: %v", err)
	}
}

func TestASpecWhoseDefaultIsNotWithinOneToMaxIsRefused(t *testing.T) {
	for _, s := range []Spec{{Default: 0, Max: 3}, {Default: 4, Max: 3}, {Default: -1, Max: 3}} {
		if _, err := s.Start(0, "", nil); err == nil || !strings.Contains(err.Error(), "0 < Default <= Max") {
			t.Errorf("Spec %+v: %v", s, err)
		}
	}
}
