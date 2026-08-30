package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scorix.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDevServerPort(t *testing.T) {
	const withDev = "name: x\ndev:\n  port: 7251\nweb:\n  port: 7250\n"
	const withoutDev = "name: x\nweb:\n  port: 7250\n"

	cases := []struct {
		name     string
		manifest string
		env      string
		want     int
	}{
		{"manifest wins over the default", withDev, "", 7251},
		{"no dev block falls back", withoutDev, "", defaultDevPort},
		{"env wins over the manifest", withDev, "9999", 9999},
		{"env alone still works", withoutDev, "9999", 9999},

		// A typo in the environment must not silently pick a different port than
		// the one the manifest registered.
		{"non-numeric env is ignored", withDev, "not-a-port", 7251},
		{"zero env is ignored", withDev, "0", 7251},
		{"out-of-range env is ignored", withDev, "70000", 7251},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SCORIX_DEV_PORT", c.env)
			if got := devServerPort(writeManifest(t, c.manifest)); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}

// web.port must never be read as the dev-server port: `-mode web` binds it, and
// `scorix dev` can be running beside that.
func TestDevServerPortIgnoresWebPort(t *testing.T) {
	t.Setenv("SCORIX_DEV_PORT", "")
	path := writeManifest(t, "name: x\nweb:\n  port: 7250\n")
	if got := devServerPort(path); got == 7250 {
		t.Fatal("devServerPort returned web.port")
	}
}

// An unreadable manifest is not a reason to fail the whole dev run.
func TestDevServerPortMissingManifest(t *testing.T) {
	t.Setenv("SCORIX_DEV_PORT", "")
	got := devServerPort(filepath.Join(t.TempDir(), "absent.yaml"))
	if got != defaultDevPort {
		t.Fatalf("got %d, want %d", got, defaultDevPort)
	}
}
