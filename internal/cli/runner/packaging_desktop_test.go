package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDesktop(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "App.desktop")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDesktopIdentityRefusesAnotherAppsExec(t *testing.T) {
	p := writeDesktop(t, "[Desktop Entry]\nName=S3Hub\nExec=RedisHub\nIcon=S3Hub\nType=Application\n")
	err := checkDesktopIdentity(p, "S3Hub")
	if err == nil {
		t.Fatal("accepted a desktop entry pointing at another app's binary")
	}
	if !strings.Contains(err.Error(), "RedisHub") || !strings.Contains(err.Error(), "S3Hub") {
		t.Errorf("error names neither side: %v", err)
	}
}

func TestDesktopIdentityRefusesAnotherAppsIcon(t *testing.T) {
	p := writeDesktop(t, "[Desktop Entry]\nName=S3Hub\nExec=S3Hub\nIcon=RedisHub\nType=Application\n")
	if err := checkDesktopIdentity(p, "S3Hub"); err == nil {
		t.Fatal("accepted a desktop entry pointing at another app's icon")
	}
}

func TestDesktopIdentityAllowsFieldCodes(t *testing.T) {
	p := writeDesktop(t, "[Desktop Entry]\nName=S3Hub\nExec=S3Hub %u\nIcon=S3Hub\nType=Application\n")
	if err := checkDesktopIdentity(p, "S3Hub"); err != nil {
		t.Fatalf("refused a valid field code: %v", err)
	}
}

func TestDesktopIdentityAllowsADifferentVisibleName(t *testing.T) {
	p := writeDesktop(t, "[Desktop Entry]\nName=S3 Hub\nExec=S3Hub\nIcon=S3Hub\nType=Application\n")
	if err := checkDesktopIdentity(p, "S3Hub"); err != nil {
		t.Fatalf("refused a different Name: %v", err)
	}
}

func TestDesktopIdentityIgnoresAMissingFile(t *testing.T) {
	if err := checkDesktopIdentity(filepath.Join(t.TempDir(), "nope.desktop"), "S3Hub"); err != nil {
		t.Fatalf("a missing file is not this check's business: %v", err)
	}
}

func TestDesktopIdentityReadsTheMainEntryNotAnAction(t *testing.T) {
	p := writeDesktop(t, "[Desktop Entry]\nName=S3Hub\nExec=RedisHub\nIcon=S3Hub\nType=Application\nActions=new-window;\n\n[Desktop Action new-window]\nName=New Window\nExec=S3Hub --new-window\n")
	err := checkDesktopIdentity(p, "S3Hub")
	if err == nil {
		t.Fatal("a later Exec hid the main entry pointing at another app")
	}
	if !strings.Contains(err.Error(), "RedisHub") {
		t.Errorf("error does not name the wrong binary: %v", err)
	}
}
