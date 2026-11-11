package migratetest

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tradalab/scorix/data"
)

func file(up string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("-- +goose Up\n" + up + "\n")}
}

type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func check(t *testing.T, run func(testing.TB)) []string {
	t.Helper()
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(r)
	}()
	<-done
	return r.errs
}

func release1() fstest.MapFS {
	return fstest.MapFS{
		"00001_init.sql": file("CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT NOT NULL);"),
	}
}

func release2() fstest.MapFS {
	fsys := release1()
	fsys["00002_tags.sql"] = file(`ALTER TABLE notes ADD COLUMN tags TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_notes_tags ON notes(tags);
CREATE VIRTUAL TABLE notes_fts USING fts5(body);
CREATE VIEW tagged AS SELECT id FROM notes WHERE tags <> '';
CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN INSERT INTO notes_fts (rowid, body) VALUES (new.id, new.body); END;`)
	return fsys
}

func migrations(fsys fstest.MapFS) data.Migrations {
	return data.Migrations{FS: fsys, Dialect: "sqlite3"}
}

func TestAnUpgradeFromEachReleaseThatMatchesAFreshDatabasePasses(t *testing.T) {
	v1 := Dump(t, migrations(release1()))
	v2 := Dump(t, migrations(release2()))
	shapes := fstest.MapFS{"v1.sql": {Data: []byte(v1)}, "v2.sql": {Data: []byte(v2)}}
	if errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(release2()), shapes) }); len(errs) != 0 {
		t.Fatalf("a sound chain was reported:\n%s", strings.Join(errs, "\n"))
	}
}

func TestABaselineEditedAfterItShippedIsReported(t *testing.T) {
	shapes := fstest.MapFS{"v1.sql": {Data: []byte(Dump(t, migrations(release1())))}}
	edited := release2()
	edited["00001_init.sql"] = file("CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT NOT NULL, pinned INTEGER NOT NULL DEFAULT 0);")
	errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(edited), shapes) })
	if len(errs) != 1 || !strings.Contains(errs[0], "v1.sql") || !strings.Contains(errs[0], "lacks column notes.pinned") {
		t.Fatalf("errs = %q", errs)
	}
}

func TestAShapeFromBeforeMigrationsIsCheckedToo(t *testing.T) {
	fsys := fstest.MapFS{
		"00001_init.sql": file("CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL, tags TEXT NOT NULL DEFAULT '');"),
	}
	shapes := fstest.MapFS{"before.sql": {Data: []byte("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL);")}}
	errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(fsys), shapes) })
	if len(errs) != 1 || !strings.Contains(errs[0], "before.sql") || !strings.Contains(errs[0], "lacks column notes.tags") {
		t.Fatalf("errs = %q", errs)
	}
}

func TestWhatOnlyTheUpgradedDatabaseHasIsReported(t *testing.T) {
	shapes := fstest.MapFS{"old.sql": {Data: []byte("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL, legacy TEXT); CREATE INDEX idx_legacy ON notes(legacy);")}}
	fsys := fstest.MapFS{"00001_init.sql": file("CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL);")}
	errs := strings.Join(check(t, func(tb testing.TB) { Upgrades(tb, migrations(fsys), shapes) }), "\n")
	for _, want := range []string{"has column notes.legacy", "has index idx_legacy"} {
		if !strings.Contains(errs, want) {
			t.Errorf("errs %q lack %q", errs, want)
		}
	}
}

func TestEachKindOfSchemaObjectIsCompared(t *testing.T) {
	for name, pair := range map[string][2]string{
		"column type":        {"CREATE TABLE d (a TEXT);", "CREATE TABLE d (a INTEGER);"},
		"not null":           {"CREATE TABLE d (a TEXT);", "CREATE TABLE d (a TEXT NOT NULL);"},
		"default":            {"CREATE TABLE d (a TEXT);", "CREATE TABLE d (a TEXT DEFAULT 'x');"},
		"primary key":        {"CREATE TABLE d (a INTEGER);", "CREATE TABLE d (a INTEGER PRIMARY KEY);"},
		"generated column":   {"CREATE TABLE d (a INTEGER, b INTEGER);", "CREATE TABLE d (a INTEGER, b INTEGER AS (a * 2));"},
		"foreign update":     {"CREATE TABLE d (n INTEGER REFERENCES notes(id));", "CREATE TABLE d (n INTEGER REFERENCES notes(id) ON UPDATE CASCADE);"},
		"index columns":      {"CREATE INDEX i ON notes(tags);", "CREATE INDEX i ON notes(body);"},
		"index column order": {"CREATE INDEX i ON notes(body, tags);", "CREATE INDEX i ON notes(tags, body);"},
		"unique index":       {"CREATE INDEX i ON notes(tags);", "CREATE UNIQUE INDEX i ON notes(tags);"},
		"descending":         {"CREATE INDEX i ON notes(tags);", "CREATE INDEX i ON notes(tags DESC);"},
		"collation":          {"CREATE INDEX i ON notes(tags);", "CREATE INDEX i ON notes(tags COLLATE NOCASE);"},
		"partial index":      {"CREATE INDEX i ON notes(tags);", "CREATE INDEX i ON notes(tags) WHERE tags <> '';"},
		"foreign key":        {"CREATE TABLE d (n INTEGER);", "CREATE TABLE d (n INTEGER REFERENCES notes(id));"},
		"foreign action":     {"CREATE TABLE d (n INTEGER REFERENCES notes(id));", "CREATE TABLE d (n INTEGER REFERENCES notes(id) ON DELETE CASCADE);"},
		"view":               {"CREATE VIEW v AS SELECT id FROM notes;", "CREATE VIEW v AS SELECT body FROM notes;"},
		"trigger":            {"CREATE TRIGGER g AFTER DELETE ON notes BEGIN SELECT 1; END;", "CREATE TRIGGER g AFTER DELETE ON notes BEGIN SELECT 2; END;"},
		"virtual table":      {"CREATE VIRTUAL TABLE f USING fts5(a);", "CREATE VIRTUAL TABLE f USING fts5(a, b);"},
		"check":              {"CREATE TABLE d (a TEXT);", "CREATE TABLE d (a TEXT CHECK (a <> ''));"},
		"column collation":   {"CREATE TABLE d (a TEXT);", "CREATE TABLE d (a TEXT COLLATE NOCASE);"},
		"strict":             {"CREATE TABLE d (a TEXT);", "CREATE TABLE d (a TEXT) STRICT;"},
		"without rowid":      {"CREATE TABLE d (a TEXT PRIMARY KEY);", "CREATE TABLE d (a TEXT PRIMARY KEY) WITHOUT ROWID;"},
		"generated as":       {"CREATE TABLE d (a INTEGER, b INTEGER AS (a * 2));", "CREATE TABLE d (a INTEGER, b INTEGER AS (a * 3));"},
		"table check":        {"CREATE TABLE d (a INTEGER, b INTEGER);", "CREATE TABLE d (a INTEGER, b INTEGER, CHECK (a < b));"},
		"upper-case column":  {"CREATE TABLE d (A TEXT);", "CREATE TABLE d (A TEXT CHECK (A <> ''));"},
		"comma in a string":  {"CREATE TABLE d (a TEXT DEFAULT 'x,y');", "CREATE TABLE d (a TEXT DEFAULT 'x,y' CHECK (a <> ''));"},
	} {
		shipped, fixed := release2(), release2()
		shipped["00003_change.sql"] = file(pair[0])
		fixed["00003_change.sql"] = file(pair[1])
		shapes := fstest.MapFS{"v3.sql": {Data: []byte(Dump(t, migrations(shipped)))}}
		if errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(fixed), shapes) }); len(errs) == 0 {
			t.Errorf("%s: a schema that differs only in it passed", name)
		}
	}
}

func TestAnUpgradeLeftAtAnotherVersionIsReported(t *testing.T) {
	ahead := release2()
	ahead["00003_noop.sql"] = file("SELECT 1;")
	shapes := fstest.MapFS{"v3.sql": {Data: []byte(Dump(t, migrations(ahead)))}}
	errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(release2()), shapes) })
	if joined := strings.Join(errs, "\n"); !strings.Contains(joined, "lacks version 2") || !strings.Contains(joined, "has version 3") {
		t.Fatalf("errs = %q", errs)
	}
}

func TestADumpRebuildsTheVersionsSoNothingRunsTwice(t *testing.T) {
	fsys := release2()
	fsys["00003_seed.sql"] = file("INSERT INTO notes (body) VALUES ('seeded');")
	shapes := fstest.MapFS{"v3.sql": {Data: []byte(Dump(t, migrations(fsys)))}}
	if errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(fsys), shapes) }); len(errs) != 0 {
		t.Fatalf("errs = %q", errs)
	}
	if dump := Dump(t, migrations(fsys)); strings.Contains(dump, "seeded") || strings.Contains(dump, "notes_fts_data") {
		t.Fatalf("a dump holds rows or FTS shadow tables:\n%s", dump)
	}
}

func TestWhatCannotCheckAnythingIsRefused(t *testing.T) {
	for want, run := range map[string]func(testing.TB){
		"no .sql file": func(tb testing.TB) { Upgrades(tb, migrations(release1()), fstest.MapFS{}) },
		"only sqlite3": func(tb testing.TB) {
			Upgrades(tb, data.Migrations{FS: release1(), Dialect: "postgres"}, fstest.MapFS{})
		},
		"does not apply": func(tb testing.TB) {
			Upgrades(tb, migrations(release2()), fstest.MapFS{"bad.sql": {Data: []byte("CREATE TABLE notes (x);")}})
		},
	} {
		if errs := strings.Join(check(t, run), "\n"); !strings.Contains(errs, want) {
			t.Errorf("%s: errs = %q", want, errs)
		}
	}
}

func TestAColumnAddedLaterButSpelledDifferentlyStillMatches(t *testing.T) {
	fsys := fstest.MapFS{"00001_init.sql": file("CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL CHECK (body <> ''), tags TEXT NOT NULL DEFAULT '' COLLATE NOCASE CHECK (length(tags) < 100));")}
	shapes := fstest.MapFS{"old.sql": {Data: []byte(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL CHECK (body <> ''));
ALTER TABLE "notes" ADD COLUMN "tags" TEXT   NOT NULL DEFAULT '' collate "nocase" check (length(tags)  < 100);`)}}
	if errs := check(t, func(tb testing.TB) { Upgrades(tb, migrations(fsys), shapes) }); len(errs) != 0 {
		t.Fatalf("the same column added by ALTER was reported:\n%s", strings.Join(errs, "\n"))
	}
}
