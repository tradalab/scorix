// Package migratetest checks that a SQLite database left by an earlier release migrates to the
// schema a fresh install gets. Save Dump at each release; Upgrades starts from every saved one.
package migratetest

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/tradalab/scorix/data"
)

const objects = `SELECT m.type, m.name, m.tbl_name, coalesce(m.sql, '') FROM sqlite_master m
LEFT JOIN pragma_table_list l ON l.name = m.tbl_name AND l.schema = 'main'
WHERE m.name NOT LIKE 'sqlite\_%' ESCAPE '\' AND coalesce(l.type, '') <> 'shadow'
ORDER BY CASE m.type WHEN 'table' THEN 0 WHEN 'view' THEN 1 WHEN 'index' THEN 2 ELSE 3 END, m.rowid`

// Upgrades fails t unless every *.sql file in shapes, a database as an earlier release left it,
// migrates by m to the schema and version a fresh database gets. Only sqlite3 is checked.
func Upgrades(t testing.TB, m data.Migrations, shapes fs.FS) {
	t.Helper()
	if m.Dialect != "sqlite3" {
		t.Fatalf("migratetest: dialect %q; only sqlite3 is checked", m.Dialect)
	}
	names, err := fs.Glob(shapes, "*.sql")
	if err != nil {
		t.Fatalf("migratetest: %v", err)
	}
	if len(names) == 0 {
		t.Fatalf("migratetest: shapes holds no .sql file, so no upgrade would be checked")
	}
	want := describe(t, migrated(t, m, "fresh", ""))
	for _, name := range names {
		seed, err := fs.ReadFile(shapes, name)
		if err != nil {
			t.Fatalf("migratetest: %v", err)
		}
		got := describe(t, migrated(t, m, name, string(seed)))
		for _, line := range want {
			if !slices.Contains(got, line) {
				t.Errorf("migratetest: %s after migrating lacks %s", name, line)
			}
		}
		for _, line := range got {
			if !slices.Contains(want, line) {
				t.Errorf("migratetest: %s after migrating has %s, which a fresh database does not", name, line)
			}
		}
	}
}

// Dump returns SQL that rebuilds the schema of a fresh database migrated by m, with its
// goose_db_version rows and no other rows; save one per release for Upgrades to start from.
func Dump(t testing.TB, m data.Migrations) string {
	t.Helper()
	db := migrated(t, m, "fresh", "")
	var b strings.Builder
	for _, o := range query(t, db, objects) {
		if o[3] != "" {
			b.WriteString(o[3] + ";\n")
		}
	}
	for _, v := range query(t, db, `SELECT id, version_id, is_applied, tstamp FROM goose_db_version ORDER BY id`) {
		fmt.Fprintf(&b, "INSERT INTO goose_db_version (id, version_id, is_applied, tstamp) VALUES (%s, %s, %s, '%s');\n",
			v[0], v[1], v[2], strings.ReplaceAll(v[3], "'", "''"))
	}
	return b.String()
}

func migrated(t testing.TB, m data.Migrations, name, seed string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.dat"))
	if err != nil {
		t.Fatalf("migratetest: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(seed); err != nil {
		t.Fatalf("migratetest: %s: %v", name, err)
	}
	if _, _, err := data.Migrate(context.Background(), db, m); err != nil {
		t.Fatalf("migratetest: %s does not apply: %v", name, err)
	}
	return db
}

func query(t testing.TB, db *sql.DB, q string, args ...any) [][]string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("migratetest: %v", err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("migratetest: %v", err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = v.String
			if !v.Valid {
				row[i] = "NULL"
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("migratetest: %v", err)
	}
	return out
}

func describe(t testing.TB, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, o := range query(t, db, objects) {
		kind, name, table, def := o[0], o[1], o[2], strings.Join(strings.Fields(o[3]), " ")
		switch {
		case name == "goose_db_version":
		case kind == "table" && strings.HasPrefix(strings.ToUpper(def), "CREATE VIRTUAL TABLE"):
			out = append(out, "virtual table "+name+": "+def)
		case kind == "table":
			columns, checks := definitions(o[3])
			for _, strict := range query(t, db, `SELECT strict FROM pragma_table_list WHERE schema = 'main' AND name = ?`, name) {
				out = append(out, fmt.Sprintf("table %s strict=%s", name, strict[0]))
			}
			for _, c := range checks {
				out = append(out, fmt.Sprintf("check on %s (%s)", name, c))
			}
			for _, c := range query(t, db, `SELECT name, type, "notnull", coalesce(dflt_value, 'NULL'), pk, hidden FROM pragma_table_xinfo(?)`, name) {
				f := columns[strings.ToLower(c[0])]
				out = append(out, fmt.Sprintf("column %s.%s %s notnull=%s default=%s pk=%s hidden=%s collate=%s check=(%s) as=(%s)",
					name, c[0], c[1], c[2], c[3], c[4], c[5], f.collate, strings.Join(f.checks, ") and ("), f.generated))
			}
			for _, f := range query(t, db, `SELECT "table", "from", coalesce("to", 'NULL'), on_update, on_delete FROM pragma_foreign_key_list(?)`, name) {
				out = append(out, fmt.Sprintf("foreign key %s.%s -> %s.%s on update %s on delete %s", name, f[1], f[0], f[2], f[3], f[4]))
			}
			for _, i := range query(t, db, `SELECT name, "unique", partial FROM pragma_index_list(?)`, name) {
				var cols []string
				for _, c := range query(t, db, `SELECT coalesce(name, '<expr>'), desc, coll FROM pragma_index_xinfo(?) WHERE key ORDER BY seqno`, i[0]) {
					cols = append(cols, fmt.Sprintf("%s desc=%s coll=%s", c[0], c[1], c[2]))
				}
				where := ""
				if i[2] == "1" {
					where = " where " + partial(t, db, i[0])
				}
				out = append(out, fmt.Sprintf("index %s on %s unique=%s (%s)%s", i[0], name, i[1], strings.Join(cols, ", "), where))
			}
		case kind == "view" || kind == "trigger":
			out = append(out, kind+" "+name+" on "+table+": "+def)
		}
	}
	version := query(t, db, `SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`)
	return append(out, "version "+version[0][0])
}

func partial(t testing.TB, db *sql.DB, index string) string {
	t.Helper()
	def := query(t, db, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, index)
	if len(def) == 0 {
		return "?"
	}
	_, where, _ := strings.Cut(strings.Join(strings.Fields(def[0][0]), " "), " WHERE ")
	return where
}

type column struct {
	collate, generated string
	checks             []string
}

func definitions(create string) (map[string]column, []string) {
	columns := map[string]column{}
	var checks []string
	open := strings.IndexByte(create, '(')
	if open < 0 {
		return columns, nil
	}
	end := closing(create, open)
	for _, item := range split(create[open+1 : end]) {
		words, groups := scan(item)
		if len(words) == 0 {
			continue
		}
		switch strings.ToUpper(words[0]) {
		case "CONSTRAINT", "CHECK", "PRIMARY", "UNIQUE", "FOREIGN":
			for _, g := range groups {
				if g.after == "CHECK" {
					checks = append(checks, g.text)
				}
			}
			continue
		}
		var c column
		for i, w := range words {
			if i > 0 && strings.ToUpper(words[i-1]) == "COLLATE" {
				c.collate = strings.ToUpper(unquote(w))
			}
		}
		for _, g := range groups {
			switch g.after {
			case "CHECK":
				c.checks = append(c.checks, g.text)
			case "AS":
				c.generated = g.text
			}
		}
		columns[strings.ToLower(unquote(words[0]))] = c
	}
	return columns, checks
}

type group struct{ after, text string }

func scan(item string) ([]string, []group) {
	var words []string
	var groups []group
	for i := 0; i < len(item); {
		switch ch := item[i]; {
		case ch == '(':
			end := closing(item, i)
			after := ""
			if len(words) > 0 {
				after = strings.ToUpper(words[len(words)-1])
			}
			groups = append(groups, group{after, strings.Join(strings.Fields(item[i+1:end]), " ")})
			i = end + 1
		case ch == '\'' || ch == '"' || ch == '`' || ch == '[':
			end := quoted(item, i)
			words = append(words, item[i:end])
			i = end
		case ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z':
			j := i
			for j < len(item) && (item[j] == '_' || item[j] >= '0' && item[j] <= '9' || item[j] >= 'A' && item[j] <= 'Z' || item[j] >= 'a' && item[j] <= 'z') {
				j++
			}
			words = append(words, item[i:j])
			i = j
		default:
			i++
		}
	}
	return words, groups
}

func split(body string) []string {
	var items []string
	depth, start := 0, 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\'', '"', '`', '[':
			i = quoted(body, i) - 1
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, strings.TrimSpace(body[start:i]))
				start = i + 1
			}
		}
	}
	return append(items, strings.TrimSpace(body[start:]))
}

func closing(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'', '"', '`', '[':
			i = quoted(s, i) - 1
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(s)
}

func quoted(s string, i int) int {
	end := s[i]
	if end == '[' {
		end = ']'
	}
	if j := strings.IndexByte(s[i+1:], end); j >= 0 {
		return i + j + 2
	}
	return len(s)
}

func unquote(name string) string {
	if len(name) >= 2 && strings.ContainsRune("\"`['", rune(name[0])) {
		return name[1 : len(name)-1]
	}
	return name
}
