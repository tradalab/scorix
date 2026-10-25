package modulets

import (
	"os"
	"path/filepath"
	"testing"
)

// CI's drift guard: a module's exposed methods and its TS fragment change together.
func TestFragmentsAreCurrent(t *testing.T) {
	if err := Generate("../..", true); err != nil {
		t.Fatal(err)
	}
}

func TestExposedCountsAMethodOnceAcrossPlatformFiles(t *testing.T) {
	dir := t.TempDir()
	for name, tag := range map[string]string{"open_android.go": "android", "open_other.go": "!android"} {
		src := "//go:build " + tag + "\n\npackage demo\n\nfunc register(m any) { module.Expose(m, \"Open\", nil) }\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := exposed(dir)
	if err != nil || len(got) != 1 || got[0].name != "Open" {
		t.Fatalf("err %v, methods %+v", err, got)
	}
}
