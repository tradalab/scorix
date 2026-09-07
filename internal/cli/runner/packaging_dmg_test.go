package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Assembling the bundle is the half of macOS packaging that runs anywhere - the
// dmg step needs hdiutil, but copying the icon and patching the plist does not.
// So it gets a test on every OS, which is the only reason this fix could be
// verified at all: nobody in the house has a Mac to open the result on.
func stageBundle(t *testing.T, withIcns bool, plist string) (*BuildContext, string) {
	t.Helper()

	root := t.TempDir()
	mac := filepath.Join(root, "installer", "mac")
	if err := os.MkdirAll(mac, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mac, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if withIcns {
		// Content does not matter here: the packager copies bytes, it does not
		// parse them. What matters is that it copies them at all, and under the
		// name the plist points at.
		if err := os.WriteFile(filepath.Join(mac, "Demo.icns"), []byte("icns\x00\x00\x00\x08"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	temp := filepath.Join(root, ".scorix", "build")
	if err := os.MkdirAll(temp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(temp, "Demo"), []byte("not really a mach-o"), 0o755); err != nil {
		t.Fatal(err)
	}

	// pkg is what newBuildContext defaults to for a project whose scorix.yaml
	// has no `package:` block at all (persona is one), and macSign dereferences
	// it - so a hand-built context needs it for the same reason production has
	// it, not to satisfy the test.
	return &BuildContext{
		pkg:         &PackageConfig{},
		Root:        root,
		ProductName: "Demo",
		Version:     "2.3.4",
		OS:          "darwin",
		Arch:        "amd64",
		Format:      "app",
		TempDir:     temp,
		BinaryName:  "Demo",
		ArtifactDir: filepath.Join(root, "artifacts"),
	}, filepath.Join(root, "artifacts", "Demo.app")
}

func TestDarwinBundleTakesTheIconAndPointsThePlistAtIt(t *testing.T) {
	bc, bundle := stageBundle(t, true, plistWithoutIcon)

	out, err := darwinPackager{}.Package(context.Background(), bc)
	if err != nil {
		t.Fatal(err)
	}
	if out != bundle {
		t.Fatalf("bundle at %q, expected %q", out, bundle)
	}

	icon := filepath.Join(bundle, "Contents", "Resources", "AppIcon.icns")
	raw, err := os.ReadFile(icon)
	if err != nil {
		t.Fatalf("the icon never reached the bundle: %v", err)
	}
	if string(raw[:4]) != "icns" {
		t.Fatalf("Resources/AppIcon.icns is not what was staged: %q", raw)
	}

	plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	// The plist in the source tree has neither key. Both have to be in the one
	// that ships, or the icon sits in Resources where Finder never looks and
	// the bundle carries no marketing version.
	for _, want := range []string{
		"<key>CFBundleIconFile</key>\n  <string>AppIcon</string>",
		"<key>CFBundleShortVersionString</key>\n  <string>2.3.4</string>",
		"<key>CFBundleVersion</key>\n  <string>2.3.4</string>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("shipped Info.plist is missing %q:\n%s", want, plist)
		}
	}
}

// No icns is a legitimate state - it just must not be a silent one, because the
// result looks like a finished build wearing the wrong icon.
func TestDarwinBundleWithoutAnIconSaysSoAndAddsNoIconKey(t *testing.T) {
	bc, bundle := stageBundle(t, false, plistWithoutIcon)

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	_, perr := darwinPackager{}.Package(context.Background(), bc)
	w.Close()
	os.Stdout = stdout

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	said := string(buf[:n])

	if perr != nil {
		t.Fatalf("a bundle with no icon must still build: %v", perr)
	}
	if !strings.Contains(said, "no macOS icon") || !strings.Contains(said, "Demo.icns") {
		t.Fatalf("packaging said nothing useful about the missing icon: %q", said)
	}

	plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	// Pointing at an icon that is not there would be worse than not pointing.
	if strings.Contains(string(plist), "CFBundleIconFile") {
		t.Error("the plist claims an icon the bundle does not carry")
	}
	if _, err := os.Stat(filepath.Join(bundle, "Contents", "Resources", "AppIcon.icns")); !os.IsNotExist(err) {
		t.Error("an AppIcon.icns appeared out of nowhere")
	}
}

// A plist that already carries the keys must come out with the version rewritten
// and nothing duplicated - the case every app hits from its second release on.
func TestDarwinBundleRewritesAnAlreadyCompletePlist(t *testing.T) {
	bc, bundle := stageBundle(t, true, plistComplete)

	// Parenthesised: inside an if statement Go reads `darwinPackager{}` as the
	// start of the condition's composite literal.
	if _, err := (darwinPackager{}).Package(context.Background(), bc); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(plist), "<key>CFBundleIconFile</key>") != 1 {
		t.Errorf("the icon key was duplicated:\n%s", plist)
	}
	if strings.Contains(string(plist), "0.0.1") {
		t.Errorf("the old version survived:\n%s", plist)
	}
}
