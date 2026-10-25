package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAndroidAppID(t *testing.T) {
	ok := map[string]string{
		"com.example.myapp":  "com.example.myapp",
		"com.example.my-app": "com.example.my_app",
		"dev.scorix.example": "dev.scorix.example",
	}
	for in, want := range ok {
		if got, err := androidAppID(in); err != nil || got != want {
			t.Errorf("androidAppID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "myapp", "com.1example.app", "com..app", "com.example.app!"} {
		if got, err := androidAppID(in); err == nil {
			t.Errorf("androidAppID(%q) = %q, want an error", in, got)
		}
	}
}

func TestAndroidVersionCode(t *testing.T) {
	cases := map[string]int{
		"1.0.0":        1_000_000,
		"v2.3.4":       2_003_004,
		"0.0.1":        1,
		"0.0.0":        1,
		"1.2":          1_002_000,
		"1.2.3-beta.1": 1_002_003,
		"1.1000.0":     1,
		"next":         1,
	}
	for in, want := range cases {
		if got := androidVersionCode(in); got != want {
			t.Errorf("androidVersionCode(%q) = %d, want %d", in, got, want)
		}
	}
	if androidVersionCode("1.10.0") <= androidVersionCode("1.9.99") {
		t.Error("1.10.0 must get a higher code than 1.9.99")
	}
}

func TestSelectAndroidABIs(t *testing.T) {
	got, err := selectAndroidABIs(nil)
	if err != nil || len(got) != 2 || got[0].Name != "arm64-v8a" || got[1].GOARCH != "amd64" {
		t.Fatalf("default ABIs = %+v, %v", got, err)
	}
	if got, err := selectAndroidABIs([]string{"armeabi-v7a"}); err != nil || got[0].GOARM != "7" {
		t.Fatalf("armeabi-v7a = %+v, %v", got, err)
	}
	if _, err := selectAndroidABIs([]string{"mips"}); err == nil {
		t.Fatal("unknown ABI accepted")
	}
}

func TestNewestNDK(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"25.1.8937393", "27.2.12479018", "27.10.1", "26.3.11579264"} {
		if err := os.MkdirAll(filepath.Join(dir, v, "toolchains", "llvm"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Not an NDK: no toolchain inside, must not win on its name.
	if err := os.MkdirAll(filepath.Join(dir, "99.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(newestNDK(dir)); got != "27.10.1" {
		t.Errorf("newestNDK = %s, want 27.10.1", got)
	}
}

// Without aapt2 the ID comes from scorix.yaml, with the debug suffix scorix.gradle adds.
func TestAPKAppIDFallsBackToTheManifest(t *testing.T) {
	t.Setenv("ANDROID_HOME", t.TempDir())
	t.Setenv("ANDROID_SDK_ROOT", "")
	root := t.TempDir()
	for _, c := range []struct {
		yaml  string
		debug bool
		want  string
	}{
		{"app:\n  name: x\n  identifier: com.acme.app\n", false, "com.acme.app"},
		{"app:\n  name: x\n  identifier: com.acme.app\n", true, "com.acme.app.dev"},
		{"app:\n  name: x\n  identifier: com.acme.app\nandroid:\n  debug_id_suffix: \"\"\n", true, "com.acme.app"},
	} {
		if err := os.WriteFile(filepath.Join(root, "scorix.yaml"), []byte(c.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := apkAppID(context.Background(), "none.apk", root, c.debug); err != nil || got != c.want {
			t.Errorf("apkAppID(debug=%v) = %q, %v; want %q", c.debug, got, err, c.want)
		}
	}
}

func TestFindAAPT2PicksNewestBuildTools(t *testing.T) {
	sdk := t.TempDir()
	t.Setenv("ANDROID_HOME", sdk)
	name := "aapt2"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if got := findAAPT2(); got != "" {
		t.Fatalf("findAAPT2 with no build-tools = %q", got)
	}
	for _, v := range []string{"34.0.0", "36.0.0", "35.0.1"} {
		if err := os.MkdirAll(filepath.Join(sdk, "build-tools", v), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sdk, "build-tools", v, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sdk, "build-tools", "37.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := findAAPT2(), filepath.Join(sdk, "build-tools", "36.0.0", name); got != want {
		t.Errorf("findAAPT2 = %q, want %q", got, want)
	}
}

func TestMobilePlatform(t *testing.T) {
	if err := checkMobilePlatform("android"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"ios", "windows", ""} {
		err := checkMobilePlatform(p)
		ee, ok := err.(*ExitError)
		if !ok || ee.Code != ExitUsage {
			t.Errorf("checkMobilePlatform(%q) = %v, want a usage error", p, err)
		}
	}
}

func TestWriteAndroidVersion(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		version, want string
	}{
		{"1.0.0", "versionName=1.0.0\nversionCode=1000000\n"},
		{"1.0.1", "versionName=1.0.1\nversionCode=1000001\n"},
	} {
		if err := writeAndroidVersion(dir, c.version); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, ".scorix", "version.properties"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.version, got, c.want)
		}
	}
}

func TestDebugIDSuffixMustBeDotLed(t *testing.T) {
	for s, ok := range map[string]bool{".dev": true, ".dev.qa": true, "": true, "dev": false, "-dev": false, ".1x": false} {
		cfg := &ProjectConfig{Android: &AndroidConfig{DebugIDSuffix: &s}}
		if err := checkIDSuffix(cfg); (err == nil) != ok {
			t.Errorf("checkIDSuffix(%q) = %v", s, err)
		}
	}
	if got := groovyString("a\nb'c"); got != `a\nb\'c` {
		t.Errorf("groovyString = %q", got)
	}
}
