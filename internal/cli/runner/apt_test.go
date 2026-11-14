package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type aptKeys struct {
	public  []byte
	private []byte
}

func genAptKey(t *testing.T, uid string) aptKeys {
	t.Helper()
	if !hasTool("gpg") || !hasTool("gpgv") {
		t.Skip("gpg and gpgv are needed")
	}
	home := filepath.Join(t.TempDir(), "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("gpg", append([]string{"--homedir", home, "--batch", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("gpg %v: %v", args, err)
		}
		return out
	}
	run("--quick-gen-key", uid, "ed25519", "sign", "never")
	keys := aptKeys{public: run("--armor", "--export", uid), private: run("--armor", "--export-secret-keys", uid)}
	t.Cleanup(func() { _ = exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run() })
	return keys
}

func aptProject(t *testing.T, public []byte) (string, *BuildContext) {
	t.Helper()
	root := t.TempDir()
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "MyApp"), []byte("ELFELFELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "installer", "linux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "installer", "linux", "app.png"), icon.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "installer", "linux", "apt.asc"), public, 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "package:\n  linux:\n    apt:\n      url: https://cdn.example.com/myapp/apt/\n      public_key: installer/linux/apt.asc\n"
	if err := os.WriteFile(filepath.Join(root, "scorix.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return root, &BuildContext{
		Root: root, TempDir: tempDir, BinaryName: "MyApp", ArtifactDir: filepath.Join(root, "artifacts"),
		ProductName: "MyApp", Version: "1.2.3", Arch: "amd64",
		Manufacturer: "TradaLab", Description: "Demo app", Identifier: "com.tradalab.myapp",
		pkg: cfg.Package,
	}
}

func TestADebWithAptConfigShipsItsSourcesListAndKeyring(t *testing.T) {
	keys := genAptKey(t, "Deb Test <deb@example.com>")
	_, bc := aptProject(t, keys.public)
	out, err := buildDeb(bc)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	members := parseAr(t, raw)
	ctrl := readTarGz(t, members[1].body)
	if got := string(ctrl["./conffiles"]); got != "/etc/apt/sources.list.d/myapp.list\n" {
		t.Fatalf("conffiles = %q", got)
	}
	data := readTarGz(t, members[2].body)
	want := "deb [signed-by=/usr/share/keyrings/myapp-archive-keyring.asc] https://cdn.example.com/myapp/apt stable main\n"
	if got := string(data["./etc/apt/sources.list.d/myapp.list"]); got != want {
		t.Fatalf("sources list = %q", got)
	}
	if !bytes.Equal(data["./usr/share/keyrings/myapp-archive-keyring.asc"], keys.public) {
		t.Fatal("the keyring in the .deb is not the configured public key")
	}
}

func TestADebWithoutAptConfigShipsNoSourcesList(t *testing.T) {
	keys := genAptKey(t, "Plain <plain@example.com>")
	_, bc := aptProject(t, keys.public)
	bc.pkg = nil
	out, err := buildDeb(bc)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	members := parseAr(t, raw)
	if _, ok := readTarGz(t, members[1].body)["./conffiles"]; ok {
		t.Fatal("conffiles written without package.linux.apt")
	}
	for name := range readTarGz(t, members[2].body) {
		if strings.Contains(name, "sources.list.d") || strings.Contains(name, "keyrings") {
			t.Fatalf("%s shipped without package.linux.apt", name)
		}
	}
}

func TestAptWritesASignedRepositoryThatGpgvAccepts(t *testing.T) {
	keys := genAptKey(t, "Repo Test <repo@example.com>")
	root, bc := aptProject(t, keys.public)
	deb, err := buildDeb(bc)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(aptKeyEnv, base64.StdEncoding.EncodeToString(keys.private))
	res := &AptResult{}
	if err := apt(context.Background(), AptOptions{Dir: root, URL: "https://cdn.example.com/myapp/apt"}, res); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "artifacts", "apt")
	if len(res.Packages) != 1 || res.Packages[0] != "pool/main/m/myapp/myapp_1.2.3_amd64.deb" {
		t.Fatalf("packages = %v", res.Packages)
	}
	debData, _ := os.ReadFile(deb)
	pooled, err := os.ReadFile(filepath.Join(repo, "pool", "main", "m", "myapp", "myapp_1.2.3_amd64.deb"))
	if err != nil || !bytes.Equal(pooled, debData) {
		t.Fatalf("pool copy differs from the artifact: %v", err)
	}

	packages, err := os.ReadFile(filepath.Join(repo, "dists", "stable", "main", "binary-amd64", "Packages"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(debData)
	for _, want := range []string{"Package: myapp", "Version: 1.2.3", "Filename: pool/main/m/myapp/myapp_1.2.3_amd64.deb", "Size: " + strconv.Itoa(len(debData)), "SHA256: " + hex.EncodeToString(sum[:])} {
		if !strings.Contains(string(packages), want) {
			t.Fatalf("Packages missing %q:\n%s", want, packages)
		}
	}

	release, err := os.ReadFile(filepath.Join(repo, "dists", "stable", "Release"))
	if err != nil {
		t.Fatal(err)
	}
	psum := sha256.Sum256(packages)
	for _, want := range []string{"Suite: stable", "Architectures: amd64", "Components: main", " " + hex.EncodeToString(psum[:]) + " " + strconv.Itoa(len(packages)) + " main/binary-amd64/Packages\n", "main/binary-amd64/Packages.gz"} {
		if !strings.Contains(string(release), want) {
			t.Fatalf("Release missing %q:\n%s", want, release)
		}
	}

	ring := filepath.Join(t.TempDir(), "ring.gpg")
	dearmor := exec.Command("gpg", "--batch", "--yes", "--dearmor", "-o", ring)
	dearmor.Stdin = bytes.NewReader(keys.public)
	if out, err := dearmor.CombinedOutput(); err != nil {
		t.Fatalf("dearmor: %v %s", err, out)
	}
	for _, args := range [][]string{
		{"--keyring", ring, filepath.Join(repo, "dists", "stable", "InRelease")},
		{"--keyring", ring, filepath.Join(repo, "dists", "stable", "Release.gpg"), filepath.Join(repo, "dists", "stable", "Release")},
	} {
		if out, err := exec.Command("gpgv", args...).CombinedOutput(); err != nil {
			t.Fatalf("gpgv %v: %v %s", args, err, out)
		}
	}
}

func TestAptRefusesAKeyThatIsNotTheOneTheDebShips(t *testing.T) {
	shipped := genAptKey(t, "Shipped <shipped@example.com>")
	other := genAptKey(t, "Other <other@example.com>")
	root, bc := aptProject(t, shipped.public)
	if _, err := buildDeb(bc); err != nil {
		t.Fatal(err)
	}
	t.Setenv(aptKeyEnv, string(other.private))
	err := apt(context.Background(), AptOptions{Dir: root}, &AptResult{})
	if err == nil || !strings.Contains(err.Error(), "does not match the public key") {
		t.Fatalf("err = %v", err)
	}
}

func TestAptRefusedForTheWrongKeyLeavesNoSignedRepositoryToUpload(t *testing.T) {
	shipped := genAptKey(t, "Shipped <shipped@example.com>")
	other := genAptKey(t, "Other <other@example.com>")
	root, bc := aptProject(t, shipped.public)
	if _, err := buildDeb(bc); err != nil {
		t.Fatal(err)
	}
	t.Setenv(aptKeyEnv, string(other.private))
	if err := apt(context.Background(), AptOptions{Dir: root}, &AptResult{}); err == nil {
		t.Fatal("apt signed with a key the .deb does not ship")
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts", "apt")); !os.IsNotExist(err) {
		t.Fatalf("artifacts/apt survived the refusal (stat err = %v): an upload step would publish a repository apt rejects", err)
	}
}

func TestAptRefusesAURLTheDebDoesNotPointAt(t *testing.T) {
	keys := genAptKey(t, "Url <url@example.com>")
	root, bc := aptProject(t, keys.public)
	if _, err := buildDeb(bc); err != nil {
		t.Fatal(err)
	}
	t.Setenv(aptKeyEnv, string(keys.private))
	err := apt(context.Background(), AptOptions{Dir: root, URL: "https://cdn.example.com/other/apt"}, &AptResult{})
	if err == nil || !strings.Contains(err.Error(), "points apt at https://cdn.example.com/myapp/apt") {
		t.Fatalf("err = %v", err)
	}
}

func TestAptNeedsTheSigningKey(t *testing.T) {
	keys := genAptKey(t, "Nokey <nokey@example.com>")
	root, bc := aptProject(t, keys.public)
	if _, err := buildDeb(bc); err != nil {
		t.Fatal(err)
	}
	t.Setenv(aptKeyEnv, "")
	err := apt(context.Background(), AptOptions{Dir: root}, &AptResult{})
	if err == nil || !strings.Contains(err.Error(), "SCORIX_APT_PRIVATE_KEY is empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestAptWithoutConfigWritesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "scorix.yaml"), []byte("package: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := &AptResult{}
	if err := apt(context.Background(), AptOptions{Dir: root}, res); err != nil {
		t.Fatal(err)
	}
	if res.Skipped == "" {
		t.Fatal("no skip reported")
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts", "apt")); !os.IsNotExist(err) {
		t.Fatal("a repository was written without package.linux.apt")
	}
}

func TestDebControlRefusesWhatIsNotADeb(t *testing.T) {
	for name, data := range map[string][]byte{
		"not ar":     []byte("PK\x03\x04zip"),
		"no control": []byte("!<arch>\ndebian-binary   0           0     0     100644  4         `\n2.0\n"),
		"cut short":  []byte("!<arch>\ncontrol.tar.gz  0           0     0     100644  999       `\nxx"),
		"xz control": []byte("!<arch>\ncontrol.tar.xz  0           0     0     100644  2         `\nxx"),
	} {
		if _, err := debControl(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
