package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wxsWith(t *testing.T, upgrade string) string {
	t.Helper()
	body := `<?xml version="1.0" encoding="utf-8"?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package
    Name="$(var.ProductName)"
    Manufacturer="$(var.Manufacturer)"
    Version="$(var.ProductVersion)"
    UpgradeCode="` + upgrade + `"
    Scope="perMachine">
  </Package>
</Wix>
`
	path := filepath.Join(t.TempDir(), "product.wxs")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The failure this exists for: s3hub shipped 1.0.0 and 1.1.0 carrying
// redishub's UpgradeCode because product.wxs was stamped from redishub with the
// literal left in. scorix.yaml had the right GUID all along and the build threw
// it away without a word - the MSI is valid, it is just a different product.
func TestCheckWxsIdentityRefusesAHardcodedUpgradeCode(t *testing.T) {
	err := checkWxsIdentity([]string{wxsWith(t, "deae281f-e7ee-41cd-bc53-b3d37b5b3737")})
	if err == nil {
		t.Fatal("a literal UpgradeCode was accepted")
	}
	for _, want := range []string{"UpgradeCode", "deae281f", "$(var.UpgradeCode)", "upgrade_code"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not say what to fix:\n%v", want, err)
		}
	}
}

func TestCheckWxsIdentityAcceptsTheVariable(t *testing.T) {
	if err := checkWxsIdentity([]string{wxsWith(t, "$(var.UpgradeCode)")}); err != nil {
		t.Fatalf("the correct form was refused: %v", err)
	}
}

// A .wxs may legitimately route identity through a variable of its own; only a
// literal can silently disagree with the manifest.
func TestCheckWxsIdentityAllowsAnotherVariable(t *testing.T) {
	if err := checkWxsIdentity([]string{wxsWith(t, "$(var.MyOwnCode)")}); err != nil {
		t.Fatalf("a different WiX variable was refused: %v", err)
	}
}

// Every identity field, not just the expensive one: a hardcoded Name or Version
// disagreeing with scorix.yaml is the same class of lie, just cheaper.
func TestCheckWxsIdentityCoversTheOtherFields(t *testing.T) {
	body := `<Wix><Package
    Name="S3Hub"
    Manufacturer="$(var.Manufacturer)"
    Version="$(var.ProductVersion)"
    UpgradeCode="$(var.UpgradeCode)"/></Wix>`
	path := filepath.Join(t.TempDir(), "product.wxs")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkWxsIdentity([]string{path})
	if err == nil || !strings.Contains(err.Error(), "Name") {
		t.Fatalf("a hardcoded Name slipped through: %v", err)
	}
}

// A missing file belongs to the scaffold path, which runs before this and
// writes the template. Erroring here would turn a first-ever package run into a
// failure instead of a scaffold.
func TestCheckWxsIdentityIgnoresAMissingFile(t *testing.T) {
	if err := checkWxsIdentity([]string{filepath.Join(t.TempDir(), "nope.wxs")}); err != nil {
		t.Fatalf("a missing file was treated as an error: %v", err)
	}
}
