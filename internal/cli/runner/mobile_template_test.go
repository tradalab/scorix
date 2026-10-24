package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Why AppTheme has a night variant: values/themes.xml in the template.
func TestAndroidTemplateThemeFollowsTheSystem(t *testing.T) {
	dir := t.TempDir()
	data := map[string]any{"Name": "demo", "Label": "Demo", "AppID": "com.example.demo", "Version": "1.0.0", "VersionCode": 1000000}
	if err := writeTemplateFS("static/mobile/android", dir, data); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "app", "src", "main", "AndroidManifest.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `android:theme="@style/AppTheme"`) {
		t.Fatal("the manifest does not use AppTheme")
	}
	for variant, parent := range map[string]string{"values": "Theme.Material.Light.NoActionBar", "values-night": "Theme.Material.NoActionBar"} {
		b, err := os.ReadFile(filepath.Join(dir, "app", "src", "main", "res", variant, "themes.xml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `name="AppTheme"`) || !strings.Contains(string(b), parent) {
			t.Fatalf("%s/themes.xml: %s", variant, b)
		}
	}
}
