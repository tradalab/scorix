package runner

import "testing"

// CI's drift guard: host.proto, hostapi.go and HostPlugins.kt change together.
func TestHostAPIIsCurrent(t *testing.T) {
	if err := GenerateHostAPI("../../..", true); err != nil {
		t.Fatal(err)
	}
}
