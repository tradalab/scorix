package llamacpp

import (
	"reflect"
	"testing"

	"github.com/tradalab/scorix/llm/probe"
)

// The asset list of ggml-org/llama.cpp release b9934, 2026-09-19.
var b9934 = []string{
	"cudart-llama-bin-win-cuda-12.4-x64.zip", "cudart-llama-bin-win-cuda-13.3-x64.zip",
	"llama-b9934-bin-android-arm64.tar.gz", "llama-b9934-bin-macos-arm64.tar.gz", "llama-b9934-bin-macos-x64.tar.gz",
	"llama-b9934-bin-ubuntu-arm64.tar.gz", "llama-b9934-bin-ubuntu-openvino-2026.2.1-x64.tar.gz",
	"llama-b9934-bin-ubuntu-rocm-7.2-x64.tar.gz", "llama-b9934-bin-ubuntu-s390x.tar.gz",
	"llama-b9934-bin-ubuntu-sycl-fp16-x64.tar.gz", "llama-b9934-bin-ubuntu-sycl-fp32-x64.tar.gz",
	"llama-b9934-bin-ubuntu-vulkan-arm64.tar.gz", "llama-b9934-bin-ubuntu-vulkan-x64.tar.gz", "llama-b9934-bin-ubuntu-x64.tar.gz",
	"llama-b9934-bin-win-cpu-arm64.zip", "llama-b9934-bin-win-cpu-x64.zip",
	"llama-b9934-bin-win-cuda-12.4-x64.zip", "llama-b9934-bin-win-cuda-13.3-x64.zip",
	"llama-b9934-bin-win-hip-radeon-x64.zip", "llama-b9934-bin-win-opencl-adreno-arm64.zip",
	"llama-b9934-bin-win-openvino-2026.2.1-x64.zip", "llama-b9934-bin-win-sycl-x64.zip", "llama-b9934-bin-win-vulkan-x64.zip",
	"llama-b9934-ui.tar.gz", "llama-b9934-xcframework.zip",
}

func nv(compute, driver string) probe.GPU {
	return probe.GPU{Vendor: "nvidia", Compute: compute, Driver: driver, Source: "nvml"}
}

func TestChooseTheBuildThisMachineRuns(t *testing.T) {
	intel := probe.GPU{Vendor: "intel", Source: "dxgi"}
	for _, tc := range []struct {
		name string
		m    probe.Machine
		opt  Options
		want Build
	}{
		{"the dev laptop, CUDA wanted", probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "610.78"), intel}}, Options{CUDA: true},
			Build{"cuda-13.3", []string{"llama-b9934-bin-win-cuda-13.3-x64.zip", "cudart-llama-bin-win-cuda-13.3-x64.zip"}}},
		{"Pascal: CUDA 13 dropped it", probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("6.1", "610.78")}}, Options{CUDA: true},
			Build{"cuda-12.4", []string{"llama-b9934-bin-win-cuda-12.4-x64.zip", "cudart-llama-bin-win-cuda-12.4-x64.zip"}}},
		{"driver too old for either", probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "531.18")}}, Options{CUDA: true},
			Build{"vulkan", []string{"llama-b9934-bin-win-vulkan-x64.zip"}}},
		{"NVIDIA seen only by DXGI", probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{{Vendor: "nvidia", Source: "dxgi"}}}, Options{CUDA: true},
			Build{"vulkan", []string{"llama-b9934-bin-win-vulkan-x64.zip"}}},
		{"CUDA not wanted", probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "610.78")}}, Options{},
			Build{"vulkan", []string{"llama-b9934-bin-win-vulkan-x64.zip"}}},
		{"Intel only", probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{intel}}, Options{CUDA: true},
			Build{"vulkan", []string{"llama-b9934-bin-win-vulkan-x64.zip"}}},
		{"no GPU", probe.Machine{OS: "windows", Arch: "amd64"}, Options{},
			Build{"cpu", []string{"llama-b9934-bin-win-cpu-x64.zip"}}},
		{"Windows on ARM has no Vulkan build", probe.Machine{OS: "windows", Arch: "arm64", GPUs: []probe.GPU{{Vendor: "qualcomm"}}}, Options{},
			Build{"cpu", []string{"llama-b9934-bin-win-cpu-arm64.zip"}}},
		{"Linux with a GPU", probe.Machine{OS: "linux", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "580.1")}}, Options{CUDA: true},
			Build{"vulkan", []string{"llama-b9934-bin-ubuntu-vulkan-x64.tar.gz"}}},
		{"Linux without one", probe.Machine{OS: "linux", Arch: "amd64"}, Options{},
			Build{"cpu", []string{"llama-b9934-bin-ubuntu-x64.tar.gz"}}},
		{"Apple silicon", probe.Machine{OS: "darwin", Arch: "arm64"}, Options{CUDA: true},
			Build{"metal", []string{"llama-b9934-bin-macos-arm64.tar.gz"}}},
	} {
		got, err := Choose(tc.m, b9934, tc.opt)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	// A CUDA build whose runtime is not in the release cannot start; the next
	// one down that has its runtime can.
	var noCudart13 []string
	for _, a := range b9934 {
		if a != "cudart-llama-bin-win-cuda-13.3-x64.zip" {
			noCudart13 = append(noCudart13, a)
		}
	}
	laptop := probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "610.78")}}
	if got, err := Choose(laptop, noCudart13, Options{CUDA: true}); err != nil || got.Backend != "cuda-12.4" {
		t.Errorf("without cudart 13.3: %+v, %v", got, err)
	}
	if _, err := Choose(probe.Machine{OS: "freebsd", Arch: "amd64"}, b9934, Options{}); err == nil {
		t.Error("an OS with no build was given one")
	}
	if _, err := Choose(probe.Machine{OS: "darwin", Arch: "arm64"}, []string{"llama-b1-bin-win-cpu-x64.zip"}, Options{}); err == nil {
		t.Error("a release without a macOS build gave one")
	}
}

// Offline, only the build Choose would pick counts as installed.
func TestPreferredMatchesChoose(t *testing.T) {
	old := probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "531.18")}}
	if got := preferred(old, Options{CUDA: true}); got[0] != "vulkan" {
		t.Errorf("a driver no CUDA build runs on prefers %v", got)
	}
	current := probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "610.78")}}
	if got := preferred(current, Options{CUDA: true}); got[0] != "cuda-*" {
		t.Errorf("a CUDA-capable card prefers %v", got)
	}
	if got := preferred(probe.Machine{OS: "linux"}, Options{}); !reflect.DeepEqual(got, []string{"cpu"}) {
		t.Errorf("no GPU prefers %v", got)
	}
}

func TestAtLeast(t *testing.T) {
	for _, tc := range []struct {
		v            string
		major, minor int
		want         bool
	}{{"610.78", 551, 61, true}, {"551.61", 551, 61, true}, {"551.52", 551, 61, false}, {"7.5", 7, 5, true}, {"6.1", 7, 5, false}, {"", 1, 0, false}, {"x", 0, 0, false}} {
		if got := atLeast(tc.v, tc.major, tc.minor); got != tc.want {
			t.Errorf("atLeast(%q, %d.%d) = %v", tc.v, tc.major, tc.minor, got)
		}
	}
}
