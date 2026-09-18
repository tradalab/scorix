package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type fixedProvider struct{ res *Result }

func (p *fixedProvider) CheckForUpdate(context.Context, string, string) (*Result, error) {
	return p.res, nil
}

func updaterOfferedAnUpdate(t *testing.T) *UpdaterModule {
	t.Helper()
	pubB64, priv := genKey(t)
	artifact := []byte("installer bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(artifact)
	}))
	t.Cleanup(srv.Close)

	m := &UpdaterModule{
		cfg:     Config{PublicKeyBase64: pubB64, CurrentVersion: "v1.0.0"},
		dataDir: t.TempDir(),
	}
	m.provider = &fixedProvider{res: &Result{
		HasUpdate:    true,
		NewVersion:   "v2.0.0",
		ArtifactURLs: []string{srv.URL + "/app.msi"},
		SigBase64:    base64.StdEncoding.EncodeToString(ed25519.Sign(priv, artifact)),
	}}
	return m
}

// The dir is disarmed before the installer runs, because the installer may own the file.
// One that returned an error owns nothing, and every retry leaves another copy in TEMP.
func TestFullUpdateRemovesTheArtifactWhenTheInstallerFails(t *testing.T) {
	m := updaterOfferedAnUpdate(t)

	var handed string
	old := runInstaller
	runInstaller = func(_ context.Context, path string, _ bool) error {
		handed = path
		return errors.New("user cancelled")
	}
	t.Cleanup(func() { runInstaller = old })

	res, err := m.FullUpdate(context.Background())
	if err == nil {
		t.Fatal("a failed installer came back as success")
	}
	if handed == "" {
		t.Fatal("the installer was never called")
	}
	if _, statErr := os.Stat(filepath.Dir(handed)); statErr == nil {
		t.Errorf("the download dir %s survived an install that failed", filepath.Dir(handed))
	}
	if res.LocalPath != "" {
		t.Errorf("result still names %q after a failed install", res.LocalPath)
	}
}

// An installer can fail for an ordinary reason: the user closes the UAC prompt.
// Raising the floor before that is decided retires a version nobody installed.
func TestFullUpdateKeepsTheFloorWhenTheInstallerFails(t *testing.T) {
	m := updaterOfferedAnUpdate(t)

	old := runInstaller
	runInstaller = func(context.Context, string, bool) error { return errors.New("user cancelled") }
	t.Cleanup(func() { runInstaller = old })

	if _, err := m.FullUpdate(context.Background()); err == nil {
		t.Fatal("a failed installer came back as success")
	}
	if floor := m.readFloor(); floor != "" {
		t.Errorf("floor = %q after an install that never happened", floor)
	}
	res, err := m.CheckForUpdate(context.Background())
	if err != nil || res == nil || !res.HasUpdate {
		t.Errorf("the update is gone after one failed attempt: %v %+v", err, res)
	}
}

// app.version has no validation rule and is env-overridable, so it can be empty or not
// a version at all - and then every comparison answers false: up to date, forever.
func TestCheckForUpdateSaysSoWhenTheAppVersionIsUnusable(t *testing.T) {
	for _, bad := range []string{"", "dev", "2026.09.18"} {
		m := &UpdaterModule{cfg: Config{CurrentVersion: bad}, dataDir: t.TempDir()}
		m.provider = &fixedProvider{res: &Result{HasUpdate: true, NewVersion: "v2.0.0"}}
		_, err := m.CheckForUpdate(context.Background())
		if err == nil || errors.Is(err, ErrNoUpdate) {
			t.Errorf("app.version %q answered %v, which reads as up to date", bad, err)
		}
	}
}

// The other half: an install that got as far as the installer must hold the floor,
// because the process can be replaced mid-call and never come back to write it.
func TestFullUpdateRaisesTheFloorWhenTheInstallerRuns(t *testing.T) {
	m := updaterOfferedAnUpdate(t)

	var sawFloor string
	old := runInstaller
	runInstaller = func(context.Context, string, bool) error {
		sawFloor = m.readFloor() // what a process that never returns would leave behind
		return nil
	}
	t.Cleanup(func() { runInstaller = old })

	if _, err := m.FullUpdate(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if sawFloor != "v2.0.0" {
		t.Errorf("floor during the install = %q, want v2.0.0", sawFloor)
	}
	if floor := m.readFloor(); floor != "v2.0.0" {
		t.Errorf("floor after the install = %q, want v2.0.0", floor)
	}
}
