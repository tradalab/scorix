//go:build !windows && !linux

package probe

// macOS has no NVML and no DXGI; its GPU is the one llama.cpp's Metal build
// uses, with no choice to make.
func nvmlGPUs() ([]GPU, error) { return nil, nil }

func dxgiGPUs() ([]GPU, error) { return nil, nil }
