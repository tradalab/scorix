package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestValidateLocalPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.csv")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := validateLocalPath("  " + file + "  "); err != nil || got != file {
		t.Fatalf("valid path: got=%q err=%v", got, err)
	}

	if _, err := validateLocalPath("report.csv"); fault.CodeOf(err) != "invalid_path" {
		t.Fatalf("relative path err = %v, want invalid_path", err)
	}
	if _, err := validateLocalPath(""); fault.CodeOf(err) != "invalid_path" {
		t.Fatalf("empty path err = %v, want invalid_path", err)
	}
	if _, err := validateLocalPath(filepath.Join(dir, "missing.txt")); fault.CodeOf(err) != fault.CodeNotFound {
		t.Fatalf("missing path err = %v, want not_found", err)
	}
	if runtime.GOOS == "windows" {
		if _, err := validateLocalPath(`\\server\share\x`); fault.CodeOf(err) != "invalid_path" {
			t.Fatalf("UNC path err = %v, want invalid_path", err)
		}
	}
}
