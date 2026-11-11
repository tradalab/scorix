package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"
)

func migration(up string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("-- +goose Up\n" + up + "\n")}
}

func blank(t *testing.T) *DB {
	t.Helper()
	db, err := Open(ctx, "sqlite", ":memory:", pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateAppliesWhatIsPendingInOrderAndOnlyOnce(t *testing.T) {
	db := blank(t)
	fsys := fstest.MapFS{
		"migration/00002_seed.sql": migration("INSERT INTO notes (body) VALUES ('second');"),
		"migration/00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL);"),
		"migration/README.md":      {Data: []byte("not a migration")},
	}
	m := Migrations{FS: fsys, Dir: "migration", Dialect: "sqlite3"}
	if from, to, err := db.Migrate(ctx, m); err != nil || from != 0 || to != 2 {
		t.Fatalf("first run = %d -> %d, %v", from, to, err)
	}
	if from, to, err := db.Migrate(ctx, m); err != nil || from != 2 || to != 2 {
		t.Fatalf("second run = %d -> %d, %v", from, to, err)
	}
	fsys["migration/00003_more.sql"] = migration("INSERT INTO notes (body) VALUES ('third');")
	if from, to, err := db.Migrate(ctx, m); err != nil || from != 2 || to != 3 {
		t.Fatalf("a new file = %d -> %d, %v", from, to, err)
	}
	var n int
	if err := db.QueryRowCtx(ctx, &n, `SELECT count(*) FROM notes`); err != nil || n != 2 {
		t.Fatalf("rows = %d, %v; each migration must run exactly once", n, err)
	}
}

func TestAFailingMigrationStopsThereAndKeepsTheVersionBeforeIt(t *testing.T) {
	db := blank(t)
	fsys := fstest.MapFS{
		"00001_init.sql":   migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);"),
		"00002_broken.sql": migration("INSERT INTO nowhere VALUES (1);"),
	}
	from, _, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3"})
	if err == nil || !strings.Contains(err.Error(), "00002_broken.sql") || from != 0 {
		t.Fatalf("a broken migration = from %d, %v", from, err)
	}
	fsys["00002_broken.sql"] = migration("INSERT INTO notes VALUES (1);")
	if from, to, err := db.Migrate(ctx, Migrations{FS: fsys, Dir: ".", Dialect: "sqlite3"}); err != nil || from != 1 || to != 2 {
		t.Fatalf("after the fix = %d -> %d, %v", from, to, err)
	}
}

func TestMigrateRefusesWhatItCannotDoSafely(t *testing.T) {
	db := blank(t)
	fsys := fstest.MapFS{"x/00001_init.sql": migration("SELECT 1;")}
	for want, m := range map[string]Migrations{
		`dialect "sqlite"`:       {FS: fsys, Dir: "x", Dialect: "sqlite"},
		"FS is required":         {Dir: "x", Dialect: "sqlite3"},
		"Schema is for postgres": {FS: fsys, Dir: "x", Dialect: "sqlite3", Schema: "links"},
		"not a lower-case":       {FS: fsys, Dir: "x", Dialect: "postgres", Schema: "links; DROP TABLE x"},
		"no migrations":          {FS: fsys, Dir: "y", Dialect: "sqlite3"},
		"Backups is for sqlite3": {FS: fsys, Dir: "x", Dialect: "postgres", Backups: 1},
	} {
		if _, _, err := db.Migrate(ctx, m); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func onDisk(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.dat")
	db, err := Open(ctx, "sqlite", path, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

type copyFile struct{ version, stamp, path string }

func copies(t *testing.T, path string) []copyFile {
	t.Helper()
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []copyFile
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), filepath.Base(path)+".")
		if !ok || !strings.HasSuffix(rest, ".bak") {
			continue
		}
		version, stamp, _ := strings.Cut(strings.TrimSuffix(rest, ".bak"), ".")
		out = append(out, copyFile{version, stamp, filepath.Join(dir, e.Name())})
	}
	slices.SortFunc(out, func(a, b copyFile) int { return strings.Compare(a.stamp, b.stamp) })
	return out
}

func versions(c []copyFile) []string {
	var out []string
	for _, f := range c {
		out = append(out, f.version)
	}
	return out
}

func newest(t *testing.T, path string) string {
	t.Helper()
	c := copies(t, path)
	if len(c) == 0 {
		t.Fatal("no copy was taken")
	}
	return c[len(c)-1].path
}

func notesIn(t *testing.T, path string) int {
	t.Helper()
	db, err := Open(ctx, "sqlite", "file:"+path+"?mode=ro", pool)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowCtx(ctx, &n, `SELECT count(*) FROM notes`); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefuseNewerStopsABuildThatDoesNotKnowTheDatabase(t *testing.T) {
	db := blank(t)
	fsys := fstest.MapFS{
		"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);"),
		"00002_body.sql": migration("ALTER TABLE notes ADD COLUMN body TEXT;"),
	}
	if _, _, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3"}); err != nil {
		t.Fatal(err)
	}
	delete(fsys, "00002_body.sql")
	if from, to, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3"}); err != nil || from != 2 || to != 2 {
		t.Fatalf("an old replica during a rollout = %d -> %d, %v", from, to, err)
	}
	from, _, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3", RefuseNewer: true})
	if !errors.Is(err, ErrNewerDatabase) || from != 2 || !strings.Contains(err.Error(), "version 2") || !strings.Contains(err.Error(), "up to 1") {
		t.Fatalf("an old build on a newer database = from %d, %v", from, err)
	}
}

func TestRegisteredGoMigrationsRunOnlyWhenAskedFor(t *testing.T) {
	t.Cleanup(goose.ResetGlobalMigrations)
	noop := func(context.Context, *sql.Tx) error { return nil }
	goose.AddNamedMigrationContext("00002_seed.go", func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO notes (id) VALUES (7)`)
		return err
	}, noop)
	db := blank(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);")}
	if _, to, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3"}); err != nil || to != 1 {
		t.Fatalf("without Registered = to %d, %v", to, err)
	}
	if from, to, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3", Registered: true}); err != nil || from != 1 || to != 2 {
		t.Fatalf("with Registered = %d -> %d, %v", from, to, err)
	}
	var n int
	if err := db.QueryRowCtx(ctx, &n, `SELECT count(*) FROM notes WHERE id = 7`); err != nil || n != 1 {
		t.Fatalf("rows from the Go migration = %d, %v", n, err)
	}
}

func TestBackupsCopyTheDatabaseBeforeEachUpgradeAndKeepTheNewest(t *testing.T) {
	db, path := onDisk(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT);")}
	m := Migrations{FS: fsys, Dialect: "sqlite3", Backups: 2}
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := copies(t, path); len(got) != 0 {
		t.Fatalf("a fresh install has nothing to lose, yet got %v", got)
	}
	if _, err := db.ExecCtx(ctx, `INSERT INTO notes (body) VALUES ('v1')`); err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"v1"}, {"v1", "v2"}, {"v2", "v3"}} {
		v := i + 2
		fsys[fmt.Sprintf("%05d_seed.sql", v)] = migration("INSERT INTO notes (body) VALUES ('later');")
		if _, to, err := db.Migrate(ctx, m); err != nil || to != int64(v) {
			t.Fatalf("to v%d = %d, %v", v, to, err)
		}
		if _, _, err := db.Migrate(ctx, m); err != nil {
			t.Fatal(err)
		}
		if got := versions(copies(t, path)); !slices.Equal(got, want) {
			t.Fatalf("after v%d copies = %v, want %v", v, got, want)
		}
		if n := notesIn(t, newest(t, path)); n != v-1 {
			t.Fatalf("the copy from before %05d holds %d notes, want %d", v, n, v-1)
		}
	}
}

func TestBackupsCoverADatabaseFromBeforeMigrations(t *testing.T) {
	db, path := onDisk(t)
	if _, err := db.ExecCtx(ctx, `CREATE TABLE notes (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY);")}
	if _, _, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3", Backups: 1}); err != nil {
		t.Fatal(err)
	}
	if got := versions(copies(t, path)); !slices.Equal(got, []string{"v0"}) {
		t.Fatalf("copies = %v", got)
	}
}

func TestAFailedUpgradeNamesItsCopyAndEveryCopyStaysUntilOneSucceeds(t *testing.T) {
	db, path := onDisk(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);")}
	m := Migrations{FS: fsys, Dialect: "sqlite3", Backups: 1}
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	fsys["00002_broken.sql"] = migration("INSERT INTO nowhere VALUES (1);")
	for n := 1; n <= 2; n++ {
		if _, err := db.ExecCtx(ctx, `INSERT INTO notes VALUES (?)`, n); err != nil {
			t.Fatal(err)
		}
		_, _, err := db.Migrate(ctx, m)
		if err == nil || !strings.Contains(err.Error(), newest(t, path)) {
			t.Fatalf("failed upgrade %d = %v, want its copy named", n, err)
		}
		if got := notesIn(t, newest(t, path)); got != n {
			t.Fatalf("the copy named by failure %d holds %d notes, want %d", n, got, n)
		}
		if got := len(copies(t, path)); got != n {
			t.Fatalf("after %d failures %d copies remain; a failure must prune none", n, got)
		}
	}
	fsys["00002_broken.sql"] = migration("SELECT 1;")
	if _, to, err := db.Migrate(ctx, m); err != nil || to != 2 {
		t.Fatalf("the retry = %d, %v", to, err)
	}
	if c := copies(t, path); len(c) != 1 || notesIn(t, c[0].path) != 2 {
		t.Fatalf("after the success copies = %v, want the newest one only", c)
	}
}

func TestACopyLeftByAnEarlierDatabaseIsNeverNamedAsThisOnesBackup(t *testing.T) {
	db, path := onDisk(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);")}
	m := Migrations{FS: fsys, Dialect: "sqlite3", Backups: 2}
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	db.ExecCtx(ctx, `INSERT INTO notes VALUES (1)`)
	fsys["00002_more.sql"] = migration("SELECT 1;")
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, "sqlite", path, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	delete(fsys, "00002_more.sql")
	if _, _, err := again.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	for id := range 5 {
		again.ExecCtx(ctx, `INSERT INTO notes VALUES (?)`, id+1)
	}
	fsys["00002_broken.sql"] = migration("INSERT INTO nowhere VALUES (1);")
	_, _, err = again.Migrate(ctx, m)
	if err == nil || !strings.Contains(err.Error(), newest(t, path)) {
		t.Fatalf("err = %v", err)
	}
	if n := notesIn(t, newest(t, path)); n != 5 {
		t.Fatalf("the copy named for the new database holds %d notes, want its 5", n)
	}
}

func TestBackupsOnAnInMemoryDatabaseAreSkipped(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	db := blank(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);")}
	if _, _, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3", Backups: 1}); err != nil {
		t.Fatal(err)
	}
	fsys["00002_more.sql"] = migration("SELECT 1;")
	if _, to, err := db.Migrate(ctx, Migrations{FS: fsys, Dialect: "sqlite3", Backups: 1}); err != nil || to != 2 {
		t.Fatalf("in memory = %d, %v", to, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("an in-memory database left %v in the working directory", entries)
	}
}

func TestTheCopyThisUpgradeTookIsNeverPruned(t *testing.T) {
	db, path := onDisk(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);")}
	m := Migrations{FS: fsys, Dialect: "sqlite3", Backups: 2}
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{".v5.29990101T000000.000000000.bak", ".v6.29990101T000000.000000001.bak"} {
		if err := os.WriteFile(path+v, []byte("a copy whose clock ran ahead"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fsys["00002_more.sql"] = migration("SELECT 1;")
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := versions(copies(t, path)); !slices.Contains(got, "v1") || len(got) != 2 {
		t.Fatalf("copies = %v, want the v1 copy this upgrade took and one more", got)
	}
}

func TestABackupIsNoMoreReadableThanTheDatabase(t *testing.T) {
	db, path := onDisk(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE notes (id INTEGER PRIMARY KEY);")}
	m := Migrations{FS: fsys, Dialect: "sqlite3", Backups: 1}
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	fsys["00002_more.sql"] = migration("SELECT 1;")
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(newest(t, path))
	if err != nil || got.Mode().Perm() != want.Mode().Perm() {
		t.Fatalf("backup mode = %v, database %v, %v", got.Mode().Perm(), want.Mode().Perm(), err)
	}
}

func TestAnInterruptedBackupLeavesNoCopyBehind(t *testing.T) {
	db, path := onDisk(t)
	fsys := fstest.MapFS{"00001_init.sql": migration("CREATE TABLE blobs (b BLOB);")}
	m := Migrations{FS: fsys, Dialect: "sqlite3", Backups: 1}
	if _, _, err := db.Migrate(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecCtx(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 100) INSERT INTO blobs SELECT randomblob(65536) FROM n`); err != nil {
		t.Fatal(err)
	}
	fsys["00002_more.sql"] = migration("SELECT 1;")
	cut, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for cut.Err() == nil {
			matches, _ := filepath.Glob(path + ".v1.*")
			for _, f := range matches {
				if info, err := os.Stat(f); err == nil && info.Size() > 0 {
					cancel()
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	if _, _, err := db.Migrate(cut, m); err == nil {
		t.Fatal("the backup finished before the cancel; the database is too small to interrupt it")
	}
	if got, _ := filepath.Glob(path + ".v1.*"); len(got) != 0 {
		t.Fatalf("an interrupted backup left %v, which a later run could take for a copy", got)
	}
}
