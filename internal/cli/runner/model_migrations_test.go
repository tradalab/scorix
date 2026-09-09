package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMigrationsPkg(t *testing.T, root, rel, pkg string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package " + pkg + "\n\nimport \"embed\"\n\nconst Dir = \".\"\n\n//go:embed *.sql\nvar FS embed.FS\n"
	if err := os.WriteFile(filepath.Join(dir, "migration.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The declared path becomes an import path, so anything the author may
// reasonably type has to survive the trip. "./internal/migration" used to render
// as "app/./internal/migration": generate called that a success and the compiler
// called it a malformed import path.
func TestResolveMigrationsPkg_CleansThePath(t *testing.T) {
	root := t.TempDir()
	writeMigrationsPkg(t, root, "internal/migration", "migration")

	for _, declared := range []string{"internal/migration", "./internal/migration", "internal/migration/", "internal//migration"} {
		rel, name, err := resolveMigrationsPkg(root, declared)
		if err != nil {
			t.Fatalf("%q: %v", declared, err)
		}
		if rel != "internal/migration" {
			t.Errorf("%q resolved to %q", declared, rel)
		}
		if name != "migration" {
			t.Errorf("%q named package %q", declared, name)
		}
	}
}

// The generated wiring says <name>.FS, so the name has to come from the package
// clause. Deriving it from the folder produced "dbmigrations.FS" for a package
// actually called migration, which only failed once the app was compiled.
func TestResolveMigrationsPkg_ReadsThePackageClause(t *testing.T) {
	root := t.TempDir()
	writeMigrationsPkg(t, root, "internal/db-migrations", "migration")

	_, name, err := resolveMigrationsPkg(root, "internal/db-migrations")
	if err != nil {
		t.Fatal(err)
	}
	if name != "migration" {
		t.Errorf("package name = %q, want the one in the file: migration", name)
	}
}

func TestResolveMigrationsPkg_RejectsWhatCannotBecomeAnImport(t *testing.T) {
	root := t.TempDir()
	writeMigrationsPkg(t, root, "internal/migration", "migration")
	if err := os.MkdirAll(filepath.Join(root, "internal", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"escapes the module":  "../elsewhere",
		"absolute":            "/etc/migration",
		"missing directory":   "internal/nope",
		"no Go package there": "internal/empty",
	}
	for name, declared := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolveMigrationsPkg(root, declared); err == nil {
				t.Fatalf("%q was accepted", declared)
			}
		})
	}
}
