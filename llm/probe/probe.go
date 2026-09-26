// Package probe tells what this machine can run a model on: which GPUs, how
// much memory each has, and what driver backs them. Every source is optional; a
// machine where none answers is reported as having no GPU, not as an error.
package probe

import (
	"fmt"
	"runtime"
	"strings"
)

type GPU struct {
	// "nvidia", "amd", "intel", "qualcomm", "apple" or "other".
	Vendor string
	Name   string
	// Dedicated memory, in bytes.
	VRAM int64
	// -1 when the source cannot say. Only NVML can: DXGI reports the size and
	// nothing about what other programs already hold.
	FreeVRAM int64
	Driver   string
	// NVIDIA compute capability, such as "8.6"; decides which CUDA builds run.
	Compute string
	// "nvml" or "dxgi".
	Source string
}

type Machine struct {
	OS, Arch string
	GPUs     []GPU
	// Why a source gave nothing, for a diagnostics screen: "nvml: not found".
	Skipped []string
}

func Probe() Machine {
	m := Machine{OS: runtime.GOOS, Arch: runtime.GOARCH}
	nv, err := nvmlGPUs()
	if err != nil {
		m.Skipped = append(m.Skipped, "nvml: "+err.Error())
	}
	dx, err := dxgiGPUs()
	if err != nil {
		m.Skipped = append(m.Skipped, "dxgi: "+err.Error())
	}
	m.GPUs = merge(nv, dx)
	return m
}

// NVML knows more about an NVIDIA card than DXGI does, so when it answered, the
// same card is not listed twice.
func merge(nv, dx []GPU) []GPU {
	out := append([]GPU(nil), nv...)
	for _, g := range dx {
		if g.Vendor == "nvidia" && len(nv) > 0 {
			continue
		}
		out = append(out, g)
	}
	return out
}

func (m Machine) Has(vendor string) bool {
	for _, g := range m.GPUs {
		if g.Vendor == vendor {
			return true
		}
	}
	return false
}

func vendorOf(id uint32) string {
	switch id {
	case 0x10DE:
		return "nvidia"
	case 0x1002, 0x1022:
		return "amd"
	case 0x8086:
		return "intel"
	case 0x5143:
		return "qualcomm"
	case 0x106B:
		return "apple"
	}
	return "other"
}

func cString(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// The calls NVML is queried through, as plain functions so a test can stand in
// for the library.
type nvmlAPI struct {
	init     func() int32
	shutdown func() int32
	count    func(*uint32) int32
	handle   func(uint32, *uintptr) int32
	name     func(uintptr, *byte, uint32) int32
	memory   func(uintptr, *nvmlMemoryV2) int32
	driver   func(*byte, uint32) int32
	compute  func(uintptr, *int32, *int32) int32
}

// nvmlMemory_v2_t. v1 folds the driver's reserved memory into "used", which
// makes free look smaller than nvidia-smi says; v2 splits it out.
type nvmlMemoryV2 struct {
	Version  uint32
	_        uint32
	Total    uint64
	Reserved uint64
	Free     uint64
	Used     uint64
}

const nvmlMemoryV2Version = uint32(40 | 2<<24)

func queryNVML(api nvmlAPI) ([]GPU, error) {
	if rc := api.init(); rc != 0 {
		return nil, fmt.Errorf("nvmlInit_v2 returned %d", rc)
	}
	defer api.shutdown()
	var drv [80]byte
	driver := ""
	if api.driver(&drv[0], uint32(len(drv))) == 0 {
		driver = cString(drv[:])
	}
	var n uint32
	if rc := api.count(&n); rc != 0 {
		return nil, fmt.Errorf("nvmlDeviceGetCount_v2 returned %d", rc)
	}
	var out []GPU
	for i := uint32(0); i < n; i++ {
		var h uintptr
		if api.handle(i, &h) != 0 {
			continue
		}
		g := GPU{Vendor: "nvidia", FreeVRAM: -1, Driver: driver, Source: "nvml"}
		var name [96]byte
		if api.name(h, &name[0], uint32(len(name))) == 0 {
			g.Name = cString(name[:])
		}
		mem := nvmlMemoryV2{Version: nvmlMemoryV2Version}
		if api.memory(h, &mem) == 0 {
			g.VRAM, g.FreeVRAM = int64(mem.Total), int64(mem.Free)
		}
		var major, minor int32
		if api.compute != nil && api.compute(h, &major, &minor) == 0 {
			g.Compute = fmt.Sprintf("%d.%d", major, minor)
		}
		out = append(out, g)
	}
	return out, nil
}
