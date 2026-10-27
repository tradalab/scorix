package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryCollectedInstallerHasAPlatform(t *testing.T) {
	for _, ext := range installerExts {
		for _, name := range []string{"App-1.2.3-windows-amd64", "App-1.2.3-darwin-arm64", "App-1.2.3"} {
			for _, cased := range []string{ext, strings.ToUpper(ext)} {
				if keys := platformKeysForArtifact(name + cased); len(keys) == 0 {
					t.Errorf("%s is collected as an installer but maps to no platform", name+cased)
				}
			}
		}
	}
}

func TestDebIsChecksummedButNotInTheAppcast(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"app_1.2.3_amd64.deb", "App-1.2.3.AppImage", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := listInstallers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || filepath.Base(files[1]) != "app_1.2.3_amd64.deb" {
		t.Fatalf("collected %v", files)
	}
	if keys := platformKeysForArtifact("app_1.2.3_amd64.deb"); len(keys) != 0 {
		t.Fatalf("a .deb maps to updater platforms %v", keys)
	}
}
