package env

import (
	"context"
	"runtime"
	"testing"
)

func TestInfo(t *testing.T) {
	m := New()
	m.appName = "Demo"
	m.appVersion = "1.2.3"
	info, err := m.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Platform != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Fatalf("platform/arch = %s/%s", info.Platform, info.Arch)
	}
	if info.AppName != "Demo" || info.AppVersion != "1.2.3" {
		t.Fatalf("app identity = %s/%s", info.AppName, info.AppVersion)
	}
	if runtime.GOOS == "windows" {
		if info.Locale == "" {
			t.Fatal("locale empty on windows")
		}
		if info.DarkMode == nil {
			t.Fatal("darkMode unreadable on windows")
		}
	}
}
