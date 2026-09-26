// Package llamacpp runs llama.cpp's llama-server as a runtime the app manages:
// it picks the release build this machine can use, downloads and verifies it,
// unpacks it, and starts the server, which then speaks the OpenAI protocol.
package llamacpp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/llm/probe"
)

type Options struct {
	// CUDA builds run faster on NVIDIA cards and cost 530-630 MB more to
	// download than Vulkan, which runs on every vendor's card. The app decides.
	CUDA bool
}

type Build struct {
	// "metal", "vulkan", "cpu" or "cuda-<version>". Also the install's
	// directory name.
	Backend string
	// The build, then the CUDA runtime that has to sit beside it.
	Assets []string
}

var (
	buildAsset  = regexp.MustCompile(`^llama-[^-]+-bin-(win|ubuntu|macos)-(.+)\.(zip|tar\.gz)$`)
	cudaBuild   = regexp.MustCompile(`^cuda-(\d+)\.(\d+)-x64$`)
	cudartAsset = regexp.MustCompile(`^cudart-llama-bin-win-cuda-(\d+\.\d+)-x64\.zip$`)
)

// The name llama.cpp's releases use for this OS and CPU.
func platform(m probe.Machine) (osName, arch string, ok bool) {
	switch m.OS {
	case "windows":
		osName = "win"
	case "linux":
		osName = "ubuntu"
	case "darwin":
		osName = "macos"
	default:
		return "", "", false
	}
	switch m.Arch {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", false
	}
	return osName, arch, true
}

func Choose(m probe.Machine, assets []string, opt Options) (Build, error) {
	osName, arch, ok := platform(m)
	if !ok {
		return Build{}, fmt.Errorf("llamacpp: no build for %s/%s", m.OS, m.Arch)
	}
	byRest := map[string]string{}
	for _, a := range assets {
		if p := buildAsset.FindStringSubmatch(a); p != nil && p[1] == osName {
			byRest[p[2]] = a
		}
	}
	pick := func(backend, rest string, extra ...string) (Build, bool) {
		a, ok := byRest[rest]
		if !ok {
			return Build{}, false
		}
		return Build{Backend: backend, Assets: append([]string{a}, extra...)}, true
	}

	if osName == "macos" {
		if b, ok := pick("metal", arch); ok {
			return b, nil
		}
		return Build{}, fmt.Errorf("llamacpp: this release has no macos-%s build", arch)
	}
	if opt.CUDA && osName == "win" {
		if b, ok := chooseCUDA(m, assets, byRest); ok {
			return b, nil
		}
	}
	// Any real GPU, whoever made it: Vulkan runs on all of them, and a model
	// that does not fit is fitted by llama-server itself.
	if len(m.GPUs) > 0 {
		if b, ok := pick("vulkan", "vulkan-"+arch); ok {
			return b, nil
		}
	}
	cpu := "cpu-" + arch
	if osName == "ubuntu" {
		cpu = arch
	}
	if b, ok := pick("cpu", cpu); ok {
		return b, nil
	}
	return Build{}, fmt.Errorf("llamacpp: this release has no %s build for %s", osName, arch)
}

// The newest CUDA build every NVIDIA card here can run, with its runtime.
// CUDA 13 dropped cards before Turing (compute 7.5) and wants driver 580; 12.4
// runs from Maxwell on driver 551.61.
func chooseCUDA(m probe.Machine, assets []string, byRest map[string]string) (Build, bool) {
	runtimes := map[string]string{}
	for _, a := range assets {
		if p := cudartAsset.FindStringSubmatch(a); p != nil {
			runtimes[p[1]] = a
		}
	}
	var best *Build
	bestMajor, bestMinor := -1, -1
	for rest, a := range byRest {
		p := cudaBuild.FindStringSubmatch(rest)
		if p == nil {
			continue
		}
		major, _ := strconv.Atoi(p[1])
		minor, _ := strconv.Atoi(p[2])
		version := p[1] + "." + p[2]
		cudart, ok := runtimes[version]
		if !ok || !cudaFits(m, major) {
			continue
		}
		if major > bestMajor || (major == bestMajor && minor > bestMinor) {
			best = &Build{Backend: "cuda-" + version, Assets: []string{a, cudart}}
			bestMajor, bestMinor = major, minor
		}
	}
	if best == nil {
		return Build{}, false
	}
	return *best, true
}

func cudaFits(m probe.Machine, major int) bool {
	seen := false
	for _, g := range m.GPUs {
		if g.Vendor != "nvidia" {
			continue
		}
		seen = true
		// A card seen only through DXGI has no driver or compute to check, and
		// atLeast refuses an empty one: a CUDA build that cannot start is worse
		// than a Vulkan one that can.
		switch {
		case major >= 13:
			if !atLeast(g.Compute, 7, 5) || !atLeast(g.Driver, 580, 0) {
				return false
			}
		case major == 12:
			if !atLeast(g.Driver, 551, 61) {
				return false
			}
		default:
			return false
		}
	}
	return seen
}

// "610.78" >= 551.61, "8.6" >= 7.5.
func atLeast(version string, major, minor int) bool {
	a, b, _ := strings.Cut(version, ".")
	ma, err := strconv.Atoi(a)
	if err != nil {
		return false
	}
	mi, _ := strconv.Atoi(b)
	return ma > major || (ma == major && mi >= minor)
}
