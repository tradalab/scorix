package updater

import (
	"strings"
	"testing"
)

// When the tag and the asset name disagree, installing puts the old build on disk and
// raises the floor to the new number, retiring a version the machine never ran.
func TestChooseAssetRefusesOneThatDoesNotCarryTheVersion(t *testing.T) {
	assets := []githubAsset{
		{Name: "demo-1.0.0-windows-amd64.msi", BrowserDownloadURL: "https://x/old.msi"},
		{Name: "demo-1.0.0-windows-amd64.msi.sig", BrowserDownloadURL: "https://x/old.msi.sig"},
	}
	if _, err := chooseAsset(assets, "windows-amd64", "v2.0.0"); err == nil {
		t.Fatal("a release tagged v2.0.0 accepted the 1.0.0 installer")
	} else if !strings.Contains(err.Error(), "demo-1.0.0-windows-amd64.msi") || !strings.Contains(err.Error(), "2.0.0") {
		t.Errorf("the error names neither side: %v", err)
	}
}

func TestChooseAssetTakesThePlatformAssetForTheVersion(t *testing.T) {
	assets := []githubAsset{
		{Name: "demo-1.2.0-macos-arm64.dmg", BrowserDownloadURL: "https://x/mac.dmg"},
		{Name: "demo-1.2.0-windows-amd64.msi.sig", BrowserDownloadURL: "https://x/win.msi.sig"},
		{Name: "demo-1.2.0-windows-amd64.msi", BrowserDownloadURL: "https://x/win.msi"},
	}
	got, err := chooseAsset(assets, "windows-amd64", "v1.2.0")
	if err != nil {
		t.Fatalf("a matching asset was refused: %v", err)
	}
	if got.Name != "demo-1.2.0-windows-amd64.msi" {
		t.Errorf("picked %q", got.Name)
	}
}

func TestChooseAssetSaysWhenNoAssetFitsThePlatform(t *testing.T) {
	assets := []githubAsset{{Name: "demo-1.2.0-macos-arm64.dmg"}}
	_, err := chooseAsset(assets, "windows-amd64", "v1.2.0")
	if err == nil || !strings.Contains(err.Error(), "windows-amd64") {
		t.Fatalf("err = %v, want it to name the platform", err)
	}
}
