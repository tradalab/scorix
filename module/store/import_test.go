package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/module/moduletest"
	_ "modernc.org/sqlite"
)

func settingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.dat"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`CREATE TABLE setting (id INTEGER PRIMARY KEY, "key" TEXT, "value" TEXT, deleted_at DATETIME)`,
		`INSERT INTO setting ("key", "value") VALUES ('language', 'vi'), ('autoupdate', 'false'), ('theme', 'dark')`,
		`INSERT INTO setting ("key", "value", deleted_at) VALUES ('accent', 'red', CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestASettingsTableMovesIntoTheStoreOnceWithoutOverwritingAndIsDropped(t *testing.T) {
	m := New()
	r := moduletest.Load(t, m, nil)
	if _, err := r.Call("Set", map[string]any{"key": "theme", "value": "light"}); err != nil {
		t.Fatal(err)
	}
	db := settingDB(t)

	moved, err := m.ImportTable(context.Background(), db, "setting")
	if err != nil || moved != 2 {
		t.Fatalf("moved = %d, err = %v; want 2 (theme was already in the store, accent was deleted)", moved, err)
	}
	for k, want := range map[string]string{"language": "vi", "autoupdate": "false", "theme": "light"} {
		if v, _ := m.Value(k); v != want {
			t.Errorf("%s = %v, want %s", k, v, want)
		}
	}
	if _, ok := m.Value("accent"); ok {
		t.Error("a soft-deleted row came back")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'setting'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the setting table is still there: %d, %v", n, err)
	}
	if moved, err := m.ImportTable(context.Background(), db, "setting"); err != nil || moved != 0 {
		t.Fatalf("a second start: moved = %d, err = %v", moved, err)
	}
}

func TestImportRefusesATableNameThatIsNotPlain(t *testing.T) {
	m := New()
	moduletest.Load(t, m, nil)
	if _, err := m.ImportTable(context.Background(), settingDB(t), `setting"; DROP TABLE x; --`); err == nil {
		t.Fatal("an injected table name was accepted")
	}
}

func TestATableWithoutSoftDeleteMovesEveryRow(t *testing.T) {
	m := New()
	moduletest.Load(t, m, nil)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.dat"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`CREATE TABLE setting ("key" TEXT PRIMARY KEY, "value" TEXT NOT NULL)`,
		`INSERT INTO setting VALUES ('language', 'ja'), ('last_connection', 'c1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if moved, err := m.ImportTable(context.Background(), db, "setting"); err != nil || moved != 2 {
		t.Fatalf("moved = %d, err = %v", moved, err)
	}
	if v, _ := m.Value("last_connection"); v != "c1" {
		t.Fatalf("last_connection = %v", v)
	}
}
