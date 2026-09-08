package runner

import (
	"strings"
	"testing"
)

const plistBroken = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>
  <string><no value></string>
  <key>CFBundleVersion</key>
  <string><no value></string>
</dict>
</plist>
`

const plistSibling = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>S3Hub</string>
  <key>CFBundleIdentifier</key>
  <string>com.tradalab.redishub</string>
</dict>
</plist>
`

// persona ships this today. Every step after the copy succeeds on it - codesign
// signs the bundle, hdiutil wraps it, notarize accepts it - and the failure
// lands on someone's Mac as an app that will not open.
func TestCheckPlistRefusesATemplateRenderedWithNoData(t *testing.T) {
	err := checkPlist([]byte(plistBroken), "installer/mac/Info.plist")
	if err == nil {
		t.Fatal("a plist holding <no value> was accepted")
	}
	for _, want := range []string{"no value", "scorix.yaml", "Info.plist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not say what to fix:\n%v", want, err)
		}
	}
}

func TestCheckPlistRefusesMalformedXML(t *testing.T) {
	if err := checkPlist([]byte("<plist><dict><key>A</key>"), "x.plist"); err == nil {
		t.Fatal("an unclosed plist was accepted")
	}
}

func TestCheckPlistAcceptsAGoodOne(t *testing.T) {
	if err := checkPlist([]byte(plistComplete), "x.plist"); err != nil {
		t.Fatalf("a valid plist was refused: %v", err)
	}
}

// The bundle identifier is a per-app constant living in a file that gets copied
// between apps - the same shape as the MSI UpgradeCode, on the platform where
// nothing else would catch it.
func TestApplyPlistIdentityOverwritesASiblingsIdentifier(t *testing.T) {
	out, notes := applyPlistIdentity([]byte(plistSibling), "com.tradalab.s3hub")

	if got := plistString(out, "CFBundleIdentifier"); got != "com.tradalab.s3hub" {
		t.Fatalf("identifier is %q, want the one from the manifest", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "com.tradalab.redishub") {
		t.Fatalf("an overwrite happened without saying what it replaced: %v", notes)
	}
	if err := checkPlist(out, "x.plist"); err != nil {
		t.Fatalf("the rewritten plist is no longer well-formed: %v", err)
	}
}

// Nothing to say when the file already agrees: a line printed on every build is
// a line nobody reads on the build that matters.
func TestApplyPlistIdentityIsQuietWhenItAlreadyAgrees(t *testing.T) {
	_, notes := applyPlistIdentity([]byte(plistSibling), "com.tradalab.redishub")
	if len(notes) != 0 {
		t.Fatalf("said something about a plist that was already right: %v", notes)
	}
}

func TestApplyPlistIdentityAddsAMissingKey(t *testing.T) {
	out, notes := applyPlistIdentity([]byte(plistComplete), "com.tradalab.demo")
	if got := plistString(out, "CFBundleIdentifier"); got != "com.tradalab.demo" {
		t.Fatalf("identifier is %q after adding it", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "adding") {
		t.Fatalf("adding a key was not reported: %v", notes)
	}
}

// An app with no identifier declared keeps whatever it has, loudly: overwriting
// with an empty string would break notarization in a way nobody could read.
func TestApplyPlistIdentityWarnsWhenTheManifestHasNone(t *testing.T) {
	out, notes := applyPlistIdentity([]byte(plistSibling), "")
	if got := plistString(out, "CFBundleIdentifier"); got != "com.tradalab.redishub" {
		t.Fatalf("the plist was rewritten with an empty identifier: %q", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "warning") {
		t.Fatalf("an empty app.identifier passed silently: %v", notes)
	}
}
