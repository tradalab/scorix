package runner

import "testing"

// CI's drift guard: host.proto, hostapi.go and HostPlugins.kt change together.
func TestHostAPIIsCurrent(t *testing.T) {
	if err := GenerateHostAPI("../../..", true); err != nil {
		t.Fatal(err)
	}
}

func TestHostPluginsComeFromHostProto(t *testing.T) {
	got := hostPlugins()
	for _, name := range []string{"keystore", "biometric", "clipboard", "dialog", "scanner", "browser", "share", "bluetooth"} {
		if !got[name] {
			t.Errorf("%s is missing from %v", name, got)
		}
	}
	if len(got) != 8 {
		t.Errorf("got %v", got)
	}
}
