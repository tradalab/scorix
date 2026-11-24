package runner

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestAKeypairKeygenPrintsIsOneAppcastLoadsAndItsSignatureVerifiesWithThePrintedPublicKey(t *testing.T) {
	out := captureStdout(t, func() {
		if err := GenerateKeypair(); err != nil {
			t.Error(err)
		}
	})
	pubB64 := regexp.MustCompile(`PUBLIC_KEY_B64 = (\S+)`).FindStringSubmatch(out)
	privB64 := regexp.MustCompile(`PRIVATE_KEY_B64 = (\S+)`).FindStringSubmatch(out)
	if pubB64 == nil || privB64 == nil {
		t.Fatalf("keygen output lacks a key:\n%s", out)
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64[1])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key %q: %v (%d bytes)", pubB64[1], err, len(pub))
	}
	t.Setenv("SCORIX_TEST_SIGN_KEY", privB64[1])
	priv, err := loadUpdatePrivateKey("SCORIX_TEST_SIGN_KEY")
	if err != nil || priv == nil {
		t.Fatalf("appcast refuses the key keygen printed: %v", err)
	}
	msg := []byte("artifact bytes")
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, ed25519.Sign(priv, msg)) {
		t.Fatal("the printed private key does not sign for the printed public key")
	}
	if !strings.Contains(out, "modules.updater.public_key_base_64") || !strings.Contains(out, "package.update.sign_key_env") {
		t.Errorf("keygen no longer says where each key goes:\n%s", out)
	}
	again := captureStdout(t, func() { _ = GenerateKeypair() })
	if strings.Contains(again, pubB64[1]) {
		t.Fatal("two keygen runs printed the same key")
	}
}

func TestAppcastRefusesAKeyThatIsNotAnEd25519PrivateKey(t *testing.T) {
	for name, v := range map[string]string{"not base64": "%%%", "a public key": base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))} {
		t.Setenv("SCORIX_TEST_SIGN_KEY", v)
		if _, err := loadUpdatePrivateKey("SCORIX_TEST_SIGN_KEY"); err == nil {
			t.Errorf("%s was taken as a signing key", name)
		}
	}
	t.Setenv("SCORIX_TEST_SIGN_KEY", "")
	if k, err := loadUpdatePrivateKey("SCORIX_TEST_SIGN_KEY"); k != nil || err != nil {
		t.Errorf("an unset key = %v, %v; want no key and no error (unsigned appcast)", k, err)
	}
}

func TestVersionPrefersTheLinkedValuesAndShortensTheCommit(t *testing.T) {
	old := [3]string{cliVersion, cliCommit, cliDate}
	t.Cleanup(func() { cliVersion, cliCommit, cliDate = old[0], old[1], old[2] })

	cliVersion, cliCommit, cliDate = "v9.8.7", "0123456789abcdef0123", "2026-11-24"
	vi := Version()
	if vi.Version != "v9.8.7" || vi.Commit != "0123456789abcdef0123" || vi.Date != "2026-11-24" {
		t.Fatalf("version = %+v, want the values linked in with -X", vi)
	}
	if got := vi.Short(); got != "v9.8.7 (0123456789ab)" {
		t.Errorf("Short = %q, want the commit cut to 12", got)
	}
	if vi.OS != runtime.GOOS || vi.Arch != runtime.GOARCH || vi.Go != runtime.Version() {
		t.Errorf("platform = %s/%s %s", vi.OS, vi.Arch, vi.Go)
	}
	out := captureStdout(t, PrintVersion)
	for _, want := range []string{"scorix v9.8.7 (0123456789ab)", "built:   2026-11-24", "os/arch: " + runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(out, want) {
			t.Errorf("version output lacks %q:\n%s", want, out)
		}
	}
	if got := (VersionInfo{Version: "v1.0.0"}).Short(); got != "v1.0.0" {
		t.Errorf("Short without a commit = %q", got)
	}
}

func TestATestBinaryWithNoLinkedVersionReportsDev(t *testing.T) {
	old := [3]string{cliVersion, cliCommit, cliDate}
	t.Cleanup(func() { cliVersion, cliCommit, cliDate = old[0], old[1], old[2] })
	cliVersion, cliCommit, cliDate = "", "", ""
	if v := Version().Version; v != "dev" {
		t.Fatalf("version of an unversioned build = %q, want dev", v)
	}
}

func fakeGo(t *testing.T, exit int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake go is a shell script")
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" > " + args + "\nexit " + map[int]string{0: "0", 1: "1"}[exit] + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

func TestUpgradeInstallsTheCLIAtTheRefAskedForOrLatest(t *testing.T) {
	args := fakeGo(t, 0)
	for ref, want := range map[string]string{"": "install github.com/tradalab/scorix/cmd/scorix@latest", "v0.44.0": "install github.com/tradalab/scorix/cmd/scorix@v0.44.0", "main": "install github.com/tradalab/scorix/cmd/scorix@main"} {
		captureStdout(t, func() {
			if err := Upgrade(context.Background(), ref); err != nil {
				t.Errorf("upgrade %q: %v", ref, err)
			}
		})
		b, err := os.ReadFile(args)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(b)); got != want {
			t.Errorf("upgrade %q ran go %q, want %q", ref, got, want)
		}
	}
}

func TestAFailedUpgradeNamesWhatItTriedToInstall(t *testing.T) {
	fakeGo(t, 1)
	var err error
	captureStdout(t, func() { err = Upgrade(context.Background(), "v0.0.1") })
	if err == nil || !strings.Contains(err.Error(), "github.com/tradalab/scorix/cmd/scorix@v0.0.1") {
		t.Fatalf("failed upgrade = %v, want it to name the install target", err)
	}
}

func inspectProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "scorix.yaml")
	body := "name: example.com/demo\napp:\n  name: demo\n  version: 1.2.3\nweb:\n  host: 127.0.0.1\n  port: 8080\nmodules:\n  clipboard: {}\n"
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestConfigShowsWhereEachValueCameFromEnvOverOverlayOverEmbedded(t *testing.T) {
	manifest := inspectProject(t)
	overlay := filepath.Join(filepath.Dir(manifest), "runtime.yaml")
	if err := os.WriteFile(overlay, []byte("web:\n  port: 9090\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCORIX_WEB_HOST", "0.0.0.0")
	out := captureStdout(t, func() {
		if err := ConfigResolved(manifest, overlay); err != nil {
			t.Error(err)
		}
	})
	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && f[1] == "=" {
			lines[f[0]] = l
		}
	}
	host, port, name := lines["host"], lines["port"], lines["name"]
	if !strings.Contains(host, "0.0.0.0") || !strings.Contains(host, "[env]") || !strings.Contains(host, "SCORIX_WEB_HOST") {
		t.Errorf("web.host from env:\n%s", host)
	}
	if !strings.Contains(port, "9090") || !strings.Contains(port, "[runtime_file]") {
		t.Errorf("web.port from the overlay:\n%s", port)
	}
	if !strings.Contains(name, "demo") || !strings.Contains(name, "[default]") {
		t.Errorf("app.name from the manifest:\n%s", name)
	}
	if !strings.Contains(out, "manifest: "+manifest) || !strings.Contains(out, "overlay: "+overlay) || !strings.Contains(out, "clipboard:") {
		t.Errorf("config output:\n%s", out)
	}
}

func TestConfigEnvListsTheVariablesEachSectionReads(t *testing.T) {
	manifest := inspectProject(t)
	out := captureStdout(t, func() {
		if err := ConfigEnv(manifest); err != nil {
			t.Error(err)
		}
	})
	for _, want := range []string{"[web]", "SCORIX_WEB_HOST", "SCORIX_WEB_PORT", "[app]", "SCORIX_APP_NAME", "SCORIX_MODULE_CLIPBOARD_<KEY>"} {
		if !strings.Contains(out, want) {
			t.Errorf("env surface lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigWithoutAManifestSaysSo(t *testing.T) {
	if err := ConfigResolved(filepath.Join(t.TempDir(), "missing.yaml"), ""); err == nil {
		t.Fatal("config read a manifest that does not exist")
	}
}
