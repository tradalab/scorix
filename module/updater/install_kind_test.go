package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHowACopyUpdatesFollowsHowItWasInstalled(t *testing.T) {
	aptList := func(p string) bool { return p == "/etc/apt/sources.list.d/s3hub.list" }
	none := func(string) bool { return false }
	for _, c := range []struct {
		name string
		env  installEnv
		kind string
		cmd  string
	}{
		{"windows msi", installEnv{goos: "windows", exe: `C:\Program Files\S3Hub\s3hub.exe`, exists: none}, InstallSelf, ""},
		{"mac bundle", installEnv{goos: "darwin", exe: "/Applications/S3Hub.app/Contents/MacOS/s3hub", exists: none}, InstallSelf, ""},
		{"mac dev build", installEnv{goos: "darwin", exe: "/Users/u/src/s3hub/s3hub", exists: none}, InstallManual, ""},
		{"appimage", installEnv{goos: "linux", exe: "/tmp/.mount_s3hubX/usr/bin/s3hub", appImage: "/home/u/S3Hub.AppImage", appDir: "/tmp/.mount_s3hubX", exists: aptList}, InstallSelf, ""},
		{"deb started from another AppImage's terminal", installEnv{goos: "linux", exe: "/usr/bin/s3hub", appImage: "/opt/Editor.AppImage", appDir: "/tmp/.mount_EditorY", exists: aptList}, InstallApt, "sudo apt update && sudo apt install --only-upgrade s3hub"},
		{"binary started from another AppImage's terminal", installEnv{goos: "linux", exe: "/opt/s3hub/s3hub", appImage: "/opt/Editor.AppImage", appDir: "/tmp/.mount_EditorY", exists: none}, InstallManual, ""},
		{"APPIMAGE without APPDIR", installEnv{goos: "linux", exe: "/usr/bin/s3hub", appImage: "/opt/Editor.AppImage", exists: aptList}, InstallApt, "sudo apt update && sudo apt install --only-upgrade s3hub"},
		{"deb with the apt repository", installEnv{goos: "linux", exe: "/usr/bin/s3hub", exists: aptList}, InstallApt, "sudo apt update && sudo apt install --only-upgrade s3hub"},
		{"deb without the apt repository", installEnv{goos: "linux", exe: "/usr/bin/s3hub", exists: none}, InstallManual, ""},
		{"binary outside /usr/bin", installEnv{goos: "linux", exe: "/opt/s3hub/s3hub", exists: func(string) bool { return true }}, InstallManual, ""},
	} {
		kind, cmd := detectInstall(c.env)
		if kind != c.kind || cmd != c.cmd {
			t.Errorf("%s: got %q %q, want %q %q", c.name, kind, cmd, c.kind, c.cmd)
		}
	}
}

func TestACopyInstalledByAptIsToldTheCommandAndDownloadsNothing(t *testing.T) {
	m := updaterOfferedAnUpdate(t)
	asInstalled(t, installEnv{goos: "linux", exe: "/usr/bin/s3hub", exists: func(p string) bool { return p == "/etc/apt/sources.list.d/s3hub.list" }})
	ran := false
	oldRun := runInstaller
	runInstaller = func(context.Context, string, bool) error { ran = true; return nil }
	t.Cleanup(func() { runInstaller = oldRun })

	check, err := m.CheckForUpdate(context.Background())
	if err != nil || check.Install != InstallApt || !strings.Contains(check.UpdateCommand, "--only-upgrade s3hub") {
		t.Fatalf("check = %+v, %v", check, err)
	}
	res, err := m.FullUpdate(context.Background())
	if !errors.Is(err, ErrInstalledByApt) || !strings.Contains(err.Error(), "sudo apt update") {
		t.Fatalf("err = %v, want ErrInstalledByApt carrying the command", err)
	}
	if ran || res.LocalPath != "" {
		t.Fatalf("an apt copy downloaded or ran an installer: ran=%v local=%q", ran, res.LocalPath)
	}
}

func TestSelfUpdateNeverReplacesAnAppImageThisProcessIsNotRunningFrom(t *testing.T) {
	old := installEnvNow
	t.Cleanup(func() { installEnvNow = old })
	for _, c := range []struct {
		name string
		env  installEnv
		want string
	}{
		{"own mount", installEnv{goos: "linux", exe: "/tmp/.mount_s3hubX/usr/bin/s3hub", appImage: "/home/u/S3Hub.AppImage", appDir: "/tmp/.mount_s3hubX/"}, "/home/u/S3Hub.AppImage"},
		{"inherited from an editor AppImage", installEnv{goos: "linux", exe: "/opt/s3hub/s3hub", appImage: "/opt/Editor.AppImage", appDir: "/tmp/.mount_EditorY"}, ""},
		{"mount that only shares a prefix", installEnv{goos: "linux", exe: "/tmp/.mount_s3hubXY/usr/bin/s3hub", appImage: "/opt/Editor.AppImage", appDir: "/tmp/.mount_s3hubX"}, ""},
	} {
		installEnvNow = func() installEnv { return c.env }
		if got := currentLinuxAppImage(); got != c.want {
			t.Errorf("%s: self-update would replace %q, want %q", c.name, got, c.want)
		}
	}
}

func asInstalled(t *testing.T, env installEnv) {
	t.Helper()
	old := installEnvNow
	installEnvNow = func() installEnv { return env }
	t.Cleanup(func() { installEnvNow = old })
}

func TestACopyThatCannotUpdateItselfSaysSoBeforeDownloadingAnything(t *testing.T) {
	m := updaterOfferedAnUpdate(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	m.provider.(*fixedProvider).res.ArtifactURLs = []string{srv.URL + "/S3Hub-2.0.0-linux-amd64.AppImage"}
	asInstalled(t, installEnv{goos: "linux", exe: "/opt/s3hub/s3hub", exists: func(string) bool { return false }})

	_, err := m.FullUpdate(context.Background())
	if !errors.Is(err, ErrCannotSelfUpdate) || hits != 0 {
		t.Fatalf("err = %v after %d download request(s), want ErrCannotSelfUpdate and none", err, hits)
	}
}
