//go:build !windows && (!linux || android)

package probe

// macOS has no NVML and no DXGI; its GPU is the one llama.cpp's Metal build
// uses, with no choice to make. On Android the purego NVML loader cannot build without cgo.
func nvmlGPUs() ([]GPU, error) { return nil, nil }

func dxgiGPUs() ([]GPU, error) { return nil, nil }
