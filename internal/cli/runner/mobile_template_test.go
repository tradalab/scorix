package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Why Theme.Scorix has a night variant: values/themes.xml in the host library, which every sync copies.
func TestAndroidThemeFollowsTheSystem(t *testing.T) {
	dir := t.TempDir()
	if err := writeTemplateFS("static/mobile/android", dir, map[string]any{"Name": "demo"}); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "app", "src", "main", "AndroidManifest.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `android:theme="@style/Theme.Scorix"`) {
		t.Fatal("the manifest does not use Theme.Scorix")
	}
	host := filepath.Join("..", "..", "..", "mobile", "android", "scorix", "src", "main", "res")
	for variant, parent := range map[string]string{"values": "Theme.Material.Light.NoActionBar", "values-night": "Theme.Material.NoActionBar"} {
		b, err := os.ReadFile(filepath.Join(host, variant, "themes.xml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `name="Theme.Scorix"`) || !strings.Contains(string(b), parent) {
			t.Fatalf("%s/themes.xml: %s", variant, b)
		}
	}
}

// The app's build.gradle.kts only applies scorix.gradle; what goes in it is checked here.
func TestAndroidGradleIsGenerated(t *testing.T) {
	dir := t.TempDir()
	if err := writeTemplateFS("static/mobile/android", dir, map[string]any{"Name": "demo"}); err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile(filepath.Join(dir, "app", "build.gradle.kts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `apply(from = rootProject.file(".scorix/scorix.gradle"))`) {
		t.Fatalf("app/build.gradle.kts does not apply scorix.gradle:\n%s", app)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "app", "src", "main", "AndroidManifest.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `android:label="${appLabel}"`) {
		t.Error("the manifest label is not the build type's placeholder")
	}

	empty := ""
	for _, c := range []struct {
		cfg     *ProjectConfig
		want    []string
		wantNot []string
	}{
		{&ProjectConfig{}, []string{
			`applicationId = 'com.example.demo'`,
			`versionCode = 2003004`,
			`versionName = '2.3.4'`,
			`manifestPlaceholders = [appLabel: 'It\'s \\ $demo']`,
			`applicationIdSuffix = '.dev'`,
			`manifestPlaceholders = [appLabel: 'Dev It\'s \\ $demo']`,
		}, nil},
		{&ProjectConfig{Android: &AndroidConfig{DebugIDSuffix: &empty}}, []string{`applicationId = 'com.example.demo'`}, []string{"applicationIdSuffix", "Dev "}},
	} {
		if err := os.MkdirAll(filepath.Join(dir, ".scorix"), 0o755); err != nil {
			t.Fatal(err)
		}
		bc := &BuildContext{Identifier: "com.example.demo", ProductName: `It's \ $demo`, Version: "2.3.4", cfg: c.cfg}
		if err := writeAndroidGradle(dir, bc); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, ".scorix", "scorix.gradle"))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("scorix.gradle lacks %s", w)
			}
		}
		for _, w := range c.wantNot {
			if strings.Contains(string(b), w) {
				t.Errorf("scorix.gradle has %s", w)
			}
		}
	}
}

// auth's shape: the launcher is an alias named dev.scorix.ScorixActivity.
const aliasManifest = `<manifest xmlns:android="http://schemas.android.com/apk/res/android"><application>
<activity android:name=".MainActivity" android:exported="false" />
<activity-alias android:name="dev.scorix.ScorixActivity" android:targetActivity=".MainActivity" android:exported="true">
<intent-filter><action android:name="android.intent.action.MAIN" /><category android:name="android.intent.category.LAUNCHER" /></intent-filter>
</activity-alias></application></manifest>`

func androidManifestOf(t *testing.T, yaml, appManifest string) (string, error) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "android")
	if appManifest == "" {
		if err := writeTemplateFS("static/mobile/android", dir, map[string]any{"Name": "demo"}); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(filepath.Join(dir, "app", "src", "main"), 0o755); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(filepath.Join(dir, "app", "src", "main", "AndroidManifest.xml"), []byte(appManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".scorix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scorix.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAndroidManifest(dir, &BuildContext{Root: root, cfg: cfg}); err != nil {
		return "", err
	}
	return readGenerated(t, dir, ".scorix/AndroidManifest.xml"), nil
}

func TestAndroidManifestFollowsScorixYaml(t *testing.T) {
	for _, c := range []struct {
		yaml, appManifest string
		want, wantNot     []string
	}{
		{"app:\n  name: demo\n  protocols: [demo, demo-auth]\nmodules:\n  notification:\n    enabled: true\n", "",
			[]string{"POST_NOTIFICATIONS", `<activity android:name="dev.scorix.ScorixActivity">`, `<data android:scheme="demo" />`, `<data android:scheme="demo-auth" />`}, nil},
		{"app:\n  name: demo\n  protocols: [demo]\n", aliasManifest,
			[]string{`<activity-alias android:name="dev.scorix.ScorixActivity">`, "</activity-alias>"}, []string{"<activity "}},
		{"app:\n  name: demo\n", "", nil, []string{"POST_NOTIFICATIONS", "RECEIVE_BOOT_COMPLETED", "<application>"}},
		{"app:\n  name: demo\nmodules:\n  notification:\n    enabled: true\n", "",
			[]string{"RECEIVE_BOOT_COMPLETED", `<receiver android:name="dev.scorix.NotificationAlarm"`, "<application>"}, []string{"<activity"}},
	} {
		got, err := androidManifestOf(t, c.yaml, c.appManifest)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("manifest lacks %s:\n%s", w, got)
			}
		}
		for _, w := range c.wantNot {
			if strings.Contains(got, w) {
				t.Errorf("manifest has %s:\n%s", w, got)
			}
		}
	}
}

func TestAndroidManifestRefusesABadScheme(t *testing.T) {
	for _, scheme := range []string{"MyApp", "https"} {
		if _, err := androidManifestOf(t, "app:\n  name: demo\n  protocols: ["+scheme+"]\n", ""); err == nil || !strings.Contains(err.Error(), scheme) {
			t.Errorf("%s: got %v", scheme, err)
		}
	}
}
