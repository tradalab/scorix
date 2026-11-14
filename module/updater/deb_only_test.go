package updater

import (
	"context"
	"errors"
	"testing"
)

func aptCopy() installEnv {
	return installEnv{goos: "linux", exe: "/usr/bin/s3hub", exists: func(p string) bool { return p == "/etc/apt/sources.list.d/s3hub.list" }}
}

func TestADebOnlyReleaseIsOfferedToACopyInstalledByApt(t *testing.T) {
	for name, manifest := range map[string]StaticAppcast{
		"no platform at all":       {Version: "1.9.1", Notes: "apt notes"},
		"no entry for this target": {Version: "1.9.1", Notes: "apt notes", Platforms: map[string]PlatformArtifact{"windows-amd64": {URLs: []string{"https://cdn/S3Hub-1.9.1-windows-amd64.msi"}}}},
	} {
		srv := serveManifest(t, manifest)
		second := &chainSource{res: &Result{HasUpdate: true, NewVersion: "9.9.9"}}
		m := &UpdaterModule{cfg: Config{CurrentVersion: "1.9.0", PlatformKey: "linux-amd64"}, dataDir: t.TempDir()}
		m.provider = chainOf(NewAppcastProvider(srv.URL), second)
		asInstalled(t, aptCopy())

		res, err := m.CheckForUpdate(context.Background())
		if err != nil || !res.HasUpdate || res.NewVersion != "1.9.1" || res.Notes != "apt notes" || res.Install != InstallApt || res.UpdateCommand == "" {
			t.Fatalf("%s: res = %+v, err = %v", name, res, err)
		}
		if second.called != 0 {
			t.Fatalf("%s: the mirror was asked after the primary had answered", name)
		}
	}
}

func TestADebOnlyReleaseStillOffersNothingToACopyThatCannotInstallIt(t *testing.T) {
	srv := serveManifest(t, StaticAppcast{Version: "1.9.1"})
	m := &UpdaterModule{cfg: Config{CurrentVersion: "1.9.0", PlatformKey: "linux-amd64"}, dataDir: t.TempDir(), provider: NewAppcastProvider(srv.URL)}
	asInstalled(t, installEnv{goos: "linux", exe: "/opt/s3hub/s3hub", exists: func(string) bool { return false }})
	if _, err := m.CheckForUpdate(context.Background()); !errors.Is(err, ErrPlatformNotInAppcast) {
		t.Fatalf("err = %v, want ErrPlatformNotInAppcast", err)
	}
}

func TestADebOnlyReleaseThatIsNotNewerIsUpToDate(t *testing.T) {
	srv := serveManifest(t, StaticAppcast{Version: "1.9.0"})
	m := &UpdaterModule{cfg: Config{CurrentVersion: "1.9.0", PlatformKey: "linux-amd64"}, dataDir: t.TempDir(), provider: NewAppcastProvider(srv.URL)}
	asInstalled(t, aptCopy())
	if _, err := m.CheckForUpdate(context.Background()); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("err = %v, want ErrNoUpdate", err)
	}
}
