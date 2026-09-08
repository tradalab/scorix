package runner

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// The MSI identity fields the manifest owns. A .wxs that writes a literal here
// instead of the variable throws away what `scorix.yaml` declared, and the build
// still succeeds - the wrong value is a perfectly valid GUID or string.
//
// UpgradeCode is the one that costs the most: Windows Installer treats it as
// "which product is this", so two apps sharing one are the same product to it.
// The second app then refuses to install ("a newer version is already
// installed") or, worse, UNINSTALLS the first and takes its place. Measured
// 2026-09-08: s3hub 1.0.0 and 1.1.0 shipped carrying redishub's UpgradeCode,
// because product.wxs had been stamped from redishub with the literal left in
// while scorix.yaml carried a correct, ignored GUID of its own.
var wxsIdentityFields = []struct {
	attr string
	want string
	why  string
}{
	{"UpgradeCode", "$(var.UpgradeCode)", "package.windows.upgrade_code"},
	{"Name", "$(var.ProductName)", "app.name"},
	{"Manufacturer", "$(var.Manufacturer)", "package.manufacturer"},
	{"Version", "$(var.ProductVersion)", "app.version"},
}

// checkWxsIdentity refuses a .wxs whose identity attributes are literals.
//
// The check is on the SOURCE rather than on the built MSI on purpose: it runs
// on every OS, it names the file and line to fix, and it fires before a build
// that would otherwise take minutes to produce something wrong. Reading the
// property table back out of the MSI would prove more, but it can only prove it
// after the fact, and only on Windows.
func checkWxsIdentity(files []string) error {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			// A missing file is the scaffold path's business, not this check's.
			continue
		}
		for _, field := range wxsIdentityFields {
			re := regexp.MustCompile(`(?m)^\s*` + field.attr + `\s*=\s*"([^"]*)"`)
			m := re.FindSubmatch(data)
			if m == nil {
				continue
			}
			got := string(m[1])
			if got == field.want {
				continue
			}
			// Another WiX variable is the app's own business - only a literal is
			// a value that can silently disagree with the manifest.
			if strings.HasPrefix(got, "$(") {
				continue
			}
			return fmt.Errorf(
				"%s: %s is the literal %q, so the %s from scorix.yaml is thrown away.\n"+
					"Use %s. An installer that carries another app's UpgradeCode is the same product to Windows Installer: it will refuse to install next to it, or uninstall it and take its place",
				f, field.attr, got, field.why, field.want)
		}
	}
	return nil
}
