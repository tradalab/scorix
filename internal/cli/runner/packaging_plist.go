package runner

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// noValue is what Go's text/template writes for a field it was given nothing
// for. It also happens not to be valid XML - the bare `<` opens a tag - so a
// plist carrying it cannot be read by macOS at all, and a bundle whose
// Info.plist cannot be read does not launch.
const noValue = "<no value>"

// checkPlist refuses an Info.plist that macOS could not read.
//
// Nothing downstream notices a broken one: the packager copies it, codesign
// signs the bundle around it, the .dmg builds, and the failure surfaces on
// someone's Mac as an app that will not open. Measured 2026-09-08: persona's
// installer/mac/Info.plist carries `<string><no value></string>` for
// CFBundleIdentifier and CFBundleVersion, from a scaffold rendered against a
// scorix.yaml with no `package:` block.
func checkPlist(data []byte, path string) error {
	if i := bytes.Index(data, []byte(noValue)); i >= 0 {
		return fmt.Errorf(
			"%s holds %q at byte %d: it was scaffolded from a template with no data behind it, which is not valid XML - macOS will refuse to launch the bundle.\n"+
				"Declare app.identifier and app.version (and a `package:` block) in scorix.yaml, delete the file, and let `scorix package` scaffold it again",
			path, noValue, i)
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s is not well-formed XML (%w) - macOS will refuse to launch a bundle whose Info.plist it cannot read", path, err)
		}
	}
}

// applyPlistIdentity writes the identity the manifest owns into the plist that
// ships, and says so when it had to change something.
//
// Same reasoning as the MSI UpgradeCode: a bundle identifier is a per-app
// constant sitting in a file that gets copied between apps, and nothing else
// compares it to scorix.yaml. Two apps sharing one confuses macOS' launch
// services, notarization ties to it, and a stale one from a sibling is
// invisible until something refuses to run.
//
// It is ENFORCED, not warned about: the framework already knows the value, so
// the file has no business disagreeing. Silence when they already match; a line
// on stdout when they did not, because an overwrite the author cannot see is
// how the next surprise gets built.
func applyPlistIdentity(data []byte, identifier string) ([]byte, []string) {
	var notes []string

	if identifier == "" {
		notes = append(notes, "warning: app.identifier is empty, so the bundle keeps whatever CFBundleIdentifier the plist carries - notarization and launch services key off it")
		return data, notes
	}

	if was := plistString(data, "CFBundleIdentifier"); was != identifier {
		if was == "" {
			notes = append(notes, fmt.Sprintf("==> Info.plist: adding CFBundleIdentifier %s (from scorix.yaml)", identifier))
		} else {
			notes = append(notes, fmt.Sprintf("==> Info.plist: CFBundleIdentifier was %q, using %q from scorix.yaml", was, identifier))
		}
		data = ensurePlistString(data, "CFBundleIdentifier", identifier)
	}
	return data, notes
}

// plistString reads one <key>/<string> pair, or "" when the key is absent.
func plistString(data []byte, key string) string {
	marker := "<key>" + key + "</key>"
	i := bytes.Index(data, []byte(marker))
	if i < 0 {
		return ""
	}
	rest := string(data[i+len(marker):])
	open := strings.Index(rest, "<string>")
	if open < 0 {
		return ""
	}
	close := strings.Index(rest[open:], "</string>")
	if close < 0 {
		return ""
	}
	return rest[open+len("<string>") : open+close]
}
