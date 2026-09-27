package probe

import "testing"

// TestProbeThisMachine can only fail on a machine with no real GPU, which is CI
// and nowhere else, so the rule gets a test that fails anywhere.
func TestWhatCanBeOffloadedTo(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    adapterDesc1
		want bool
	}{
		{"a discrete card", adapterDesc1{VendorID: 0x10DE, DedicatedVideoMemory: 1 << 30}, true},
		// Shared memory, no dedicated VRAM, and still the thing llama.cpp
		// offloads to on most laptops.
		{"an integrated gpu", adapterDesc1{VendorID: 0x8086, SharedSystemMemory: 1 << 30}, true},
		{"the software adapter, flagged", adapterDesc1{VendorID: 0x1414, Flags: adapterFlagSoftware}, false},
		// What a GitHub windows runner hands back: Microsoft's vendor id and no
		// flag at all.
		{"the basic render driver, unflagged", adapterDesc1{VendorID: 0x1414}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := offloadable(tc.d); got != tc.want {
				t.Errorf("offloadable = %v, want %v", got, tc.want)
			}
		})
	}
}
