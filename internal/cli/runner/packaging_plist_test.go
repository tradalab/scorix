package runner

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

const plistWithoutIcon = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>Demo</string>
  <key>CFBundleVersion</key>
  <string>0.0.1</string>
</dict>
</plist>
`

// The plist an app hand-copied before the scaffold carried these keys. Patching
// by replacement alone left it exactly as it is - no marketing version in the
// bundle, and no icon key even with an icns sitting in Resources - and the
// build said nothing either way.
func TestEnsurePlistStringAddsAMissingKey(t *testing.T) {
	out := string(patchPlistVersion([]byte(plistWithoutIcon), "1.2.3"))

	if strings.Count(out, "<key>CFBundleShortVersionString</key>") != 1 {
		t.Fatalf("short version key not added exactly once:\n%s", out)
	}
	if !strings.Contains(out, "<key>CFBundleShortVersionString</key>\n  <string>1.2.3</string>") {
		t.Fatalf("short version key added without the version:\n%s", out)
	}
	if !strings.Contains(out, "<key>CFBundleVersion</key>\n  <string>1.2.3</string>") {
		t.Fatalf("existing version key not rewritten:\n%s", out)
	}
	if strings.Index(out, "CFBundleShortVersionString") > strings.Index(out, "</dict>") {
		t.Fatal("the added key landed outside the dictionary")
	}
}

func TestEnsurePlistStringReplacesInPlaceAndStaysIdempotent(t *testing.T) {
	once := patchPlistVersion([]byte(plistWithoutIcon), "1.2.3")
	twice := patchPlistVersion(once, "1.2.3")
	if string(once) != string(twice) {
		t.Fatalf("patching twice changed the file:\n%s\n---\n%s", once, twice)
	}

	bumped := string(patchPlistVersion(twice, "2.0.0"))
	if strings.Contains(bumped, "1.2.3") {
		t.Fatalf("the old version survived a bump:\n%s", bumped)
	}
	if strings.Count(bumped, "<key>CFBundleVersion</key>") != 1 {
		t.Fatalf("the version key was duplicated:\n%s", bumped)
	}
}

// An icon in Contents/Resources that no key points at is invisible, so staging
// the icns has to bring the key with it.
func TestEnsurePlistStringAddsTheIconKey(t *testing.T) {
	out := string(ensurePlistString([]byte(plistWithoutIcon), "CFBundleIconFile", "AppIcon"))
	if !strings.Contains(out, "<key>CFBundleIconFile</key>\n  <string>AppIcon</string>") {
		t.Fatalf("icon key not added:\n%s", out)
	}
}

// A plist that is not a plist must come back untouched rather than gain a key
// in the middle of nowhere.
func TestEnsurePlistStringLeavesAFileWithNoDictAlone(t *testing.T) {
	junk := []byte("not a plist at all")
	if got := string(ensurePlistString(junk, "CFBundleIconFile", "AppIcon")); got != string(junk) {
		t.Fatalf("rewrote a file with no <dict>: %q", got)
	}
}

// Inserting into a file by byte offset is how a "small fix" produces a plist
// macOS refuses to read, and a bundle with an unreadable Info.plist does not
// launch at all. So the output is parsed, not eyeballed.
func TestEnsurePlistStringLeavesWellFormedXML(t *testing.T) {
	cases := map[string]string{
		"missing both keys": plistWithoutIcon,
		"already complete":  plistComplete,
		"nested dictionary": plistNested,
	}
	for name, in := range cases {
		out := patchPlistVersion([]byte(in), "3.1.4")
		out = ensurePlistString(out, "CFBundleIconFile", "AppIcon")

		dec := xml.NewDecoder(bytes.NewReader(out))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: patched plist is not well-formed XML: %v\n%s", name, err, out)
			}
		}

		// And the keys are inside the root dictionary, not after it.
		if strings.Index(string(out), "CFBundleIconFile") > strings.LastIndex(string(out), "</dict>") {
			t.Fatalf("%s: the icon key landed outside the dictionary:\n%s", name, out)
		}
	}
}

const plistComplete = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>Demo</string>
  <key>CFBundleVersion</key>
  <string>0.0.1</string>
  <key>CFBundleShortVersionString</key>
  <string>0.0.1</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
</dict>
</plist>
`

// A dictionary inside the root one: the insertion point is the LAST </dict>,
// and getting that backwards puts the key inside the nested dictionary where
// nothing reads it.
const plistNested = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>Demo</string>
  <key>CFBundleURLTypes</key>
  <array>
    <dict>
      <key>CFBundleURLName</key>
      <string>demo</string>
    </dict>
  </array>
</dict>
</plist>
`
