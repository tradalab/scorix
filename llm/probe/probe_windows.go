//go:build windows

package probe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// nvml.dll ships with the driver in System32; some driver versions put it only
// under NVSMI.
func nvmlGPUs() ([]GPU, error) {
	var dll *syscall.DLL
	var err error
	for _, p := range []string{
		filepath.Join(os.Getenv("SystemRoot"), "System32", "nvml.dll"),
		filepath.Join(os.Getenv("ProgramFiles"), "NVIDIA Corporation", "NVSMI", "nvml.dll"),
	} {
		if dll, err = syscall.LoadDLL(p); err == nil {
			break
		}
	}
	if dll == nil {
		return nil, errors.New("not found")
	}
	defer dll.Release()
	procs := map[string]*syscall.Proc{}
	for _, name := range []string{"nvmlInit_v2", "nvmlShutdown", "nvmlDeviceGetCount_v2", "nvmlDeviceGetHandleByIndex_v2",
		"nvmlDeviceGetName", "nvmlDeviceGetMemoryInfo_v2", "nvmlSystemGetDriverVersion", "nvmlDeviceGetCudaComputeCapability"} {
		p, err := dll.FindProc(name)
		if err != nil {
			return nil, fmt.Errorf("%s missing: driver too old", name)
		}
		procs[name] = p
	}
	call := func(name string, args ...uintptr) int32 {
		r, _, _ := procs[name].Call(args...)
		return int32(r)
	}
	return queryNVML(nvmlAPI{
		init:     func() int32 { return call("nvmlInit_v2") },
		shutdown: func() int32 { return call("nvmlShutdown") },
		count:    func(n *uint32) int32 { return call("nvmlDeviceGetCount_v2", uintptr(unsafe.Pointer(n))) },
		handle: func(i uint32, h *uintptr) int32 {
			return call("nvmlDeviceGetHandleByIndex_v2", uintptr(i), uintptr(unsafe.Pointer(h)))
		},
		name: func(h uintptr, b *byte, n uint32) int32 {
			return call("nvmlDeviceGetName", h, uintptr(unsafe.Pointer(b)), uintptr(n))
		},
		memory: func(h uintptr, m *nvmlMemoryV2) int32 {
			return call("nvmlDeviceGetMemoryInfo_v2", h, uintptr(unsafe.Pointer(m)))
		},
		driver: func(b *byte, n uint32) int32 {
			return call("nvmlSystemGetDriverVersion", uintptr(unsafe.Pointer(b)), uintptr(n))
		},
		compute: func(h uintptr, major, minor *int32) int32 {
			return call("nvmlDeviceGetCudaComputeCapability", h, uintptr(unsafe.Pointer(major)), uintptr(unsafe.Pointer(minor)))
		},
	})
}

// DXGI_ADAPTER_DESC1 as laid out on 64-bit Windows.
type adapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	LUIDLow               uint32
	LUIDHigh              int32
	Flags                 uint32
}

var iidFactory1 = syscall.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}

// Vtable slots: IUnknown 3, IDXGIObject 4, IDXGIFactory 5, then EnumAdapters1;
// IDXGIAdapter 3 after the same 7, then GetDesc1.
const (
	slotRelease       = 2
	slotEnumAdapters1 = 12
	slotGetDesc1      = 10

	dxgiNotFound        = 0x887A0002
	adapterFlagSoftware = 2
	vendorMicrosoft     = 0x1414
)

// Every vendor's card, which NVML cannot see, and how much memory each has.
func dxgiGPUs() ([]GPU, error) {
	dll, err := syscall.LoadDLL("dxgi.dll")
	if err != nil {
		return nil, err
	}
	defer dll.Release()
	create, err := dll.FindProc("CreateDXGIFactory1")
	if err != nil {
		return nil, err
	}
	var factory unsafe.Pointer
	if hr, _, _ := create.Call(uintptr(unsafe.Pointer(&iidFactory1)), uintptr(unsafe.Pointer(&factory))); uint32(hr) != 0 {
		return nil, fmt.Errorf("CreateDXGIFactory1: 0x%08x", uint32(hr))
	}
	defer comCall(factory, slotRelease)
	var out []GPU
	for i := uintptr(0); ; i++ {
		var adapter unsafe.Pointer
		hr := comCall(factory, slotEnumAdapters1, i, uintptr(unsafe.Pointer(&adapter)))
		if hr == dxgiNotFound {
			break
		}
		if hr != 0 {
			return out, fmt.Errorf("EnumAdapters1(%d): 0x%08x", i, hr)
		}
		var d adapterDesc1
		hr = comCall(adapter, slotGetDesc1, uintptr(unsafe.Pointer(&d)))
		comCall(adapter, slotRelease)
		if hr != 0 || !offloadable(d) {
			continue
		}
		out = append(out, GPU{
			Vendor:   vendorOf(d.VendorID),
			Name:     syscall.UTF16ToString(d.Description[:]),
			VRAM:     int64(d.DedicatedVideoMemory),
			FreeVRAM: -1,
			Source:   "dxgi",
		})
	}
	return out, nil
}

// The SOFTWARE flag is not enough: measured on a windows runner, the Basic
// Render Driver comes back with Flags 0. Microsoft's vendor id is what its three
// non-GPUs share. Not VRAM: an integrated GPU reports zero and runs fine.
func offloadable(d adapterDesc1) bool {
	return d.Flags&adapterFlagSoftware == 0 && d.VendorID != vendorMicrosoft
}

// The object stays an unsafe.Pointer from the moment COM hands it over, so
// nothing converts an integer back into a pointer.
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uint32 {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, slot*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return uint32(r)
}
