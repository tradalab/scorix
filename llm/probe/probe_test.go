package probe

import (
	"strings"
	"testing"
	"unsafe"
)

type fakeCard struct {
	name        string
	total, free uint64
	major       int32
	broken      bool
}

func fakeNVML(cards []fakeCard, initRC int32) nvmlAPI {
	put := func(dst *byte, n uint32, s string) {
		b := unsafe.Slice(dst, n)
		copy(b, s+"\x00")
	}
	return nvmlAPI{
		init:     func() int32 { return initRC },
		shutdown: func() int32 { return 0 },
		count:    func(n *uint32) int32 { *n = uint32(len(cards)); return 0 },
		handle: func(i uint32, h *uintptr) int32 {
			if cards[i].broken {
				return 999
			}
			*h = uintptr(i) + 1
			return 0
		},
		name: func(h uintptr, b *byte, n uint32) int32 { put(b, n, cards[h-1].name); return 0 },
		memory: func(h uintptr, m *nvmlMemoryV2) int32 {
			if m.Version != nvmlMemoryV2Version {
				return 1 // a real driver refuses a struct of the wrong version
			}
			m.Total, m.Free = cards[h-1].total, cards[h-1].free
			return 0
		},
		driver:  func(b *byte, n uint32) int32 { put(b, n, "610.78"); return 0 },
		compute: func(h uintptr, major, minor *int32) int32 { *major, *minor = cards[h-1].major, 6; return 0 },
	}
}

func TestNVMLReportsEachCardItCanRead(t *testing.T) {
	gpus, err := queryNVML(fakeNVML([]fakeCard{
		{name: "NVIDIA GeForce RTX 3050 Laptop GPU", total: 4 << 30, free: 1501 << 20, major: 8},
		{broken: true},
	}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 1 {
		t.Fatalf("gpus = %+v; a card NVML cannot open is skipped, not fatal", gpus)
	}
	g := gpus[0]
	if g.Name != "NVIDIA GeForce RTX 3050 Laptop GPU" || g.VRAM != 4<<30 || g.FreeVRAM != 1501<<20 || g.Driver != "610.78" || g.Compute != "8.6" || g.Vendor != "nvidia" {
		t.Errorf("gpu = %+v", g)
	}
	if _, err := queryNVML(fakeNVML(nil, 9)); err == nil {
		t.Error("a failed nvmlInit was not reported")
	}
}

// On Windows DXGI lists the NVIDIA card NVML already described, with less to
// say about it.
func TestTheSameCardIsNotListedTwice(t *testing.T) {
	nv := []GPU{{Vendor: "nvidia", Name: "RTX", FreeVRAM: 1, Source: "nvml"}}
	dx := []GPU{{Vendor: "nvidia", Name: "RTX", Source: "dxgi"}, {Vendor: "intel", Name: "Iris", Source: "dxgi"}}
	got := merge(nv, dx)
	if len(got) != 2 || got[0].Source != "nvml" || got[1].Vendor != "intel" {
		t.Errorf("merged = %+v", got)
	}
	// Without NVML, DXGI's word on the NVIDIA card is all there is.
	if got := merge(nil, dx); len(got) != 2 || got[0].Vendor != "nvidia" {
		t.Errorf("without nvml = %+v", got)
	}
}

func TestVendorIDs(t *testing.T) {
	for id, want := range map[uint32]string{0x10DE: "nvidia", 0x1002: "amd", 0x8086: "intel", 0x1414: "other"} {
		if got := vendorOf(id); got != want {
			t.Errorf("0x%04X = %s", id, got)
		}
	}
}

// Runs the real sources on whatever machine the test is on; what it finds is
// logged, and only what must hold anywhere is asserted.
func TestProbeThisMachine(t *testing.T) {
	m := Probe()
	t.Logf("%s/%s, skipped: %v", m.OS, m.Arch, m.Skipped)
	bySource := map[string]int{}
	for _, g := range m.GPUs {
		t.Logf("%s %q vram=%d MiB free=%d MiB driver=%q compute=%q via %s", g.Vendor, g.Name, g.VRAM>>20, g.FreeVRAM>>20, g.Driver, g.Compute, g.Source)
		if strings.Contains(g.Name, "Basic Render") {
			t.Error("the software adapter was listed as a GPU")
		}
		if g.Vendor == "nvidia" {
			bySource[g.Source]++
		}
	}
	if bySource["nvml"] > 0 && bySource["dxgi"] > 0 {
		t.Error("an NVIDIA card was listed by both sources")
	}
}
