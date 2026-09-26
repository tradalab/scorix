//go:build linux

package probe

import (
	"fmt"

	"github.com/ebitengine/purego"
)

var nvmlSymbols = []string{"nvmlInit_v2", "nvmlShutdown", "nvmlDeviceGetCount_v2", "nvmlDeviceGetHandleByIndex_v2",
	"nvmlDeviceGetName", "nvmlDeviceGetMemoryInfo_v2", "nvmlSystemGetDriverVersion", "nvmlDeviceGetCudaComputeCapability"}

// The library comes with the driver. Each symbol is looked up before it is
// bound: RegisterLibFunc on a name the driver lacks panics, and a wrong name
// still builds and passes vet.
func nvmlGPUs() ([]GPU, error) {
	lib, err := purego.Dlopen("libnvidia-ml.so.1", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("not found")
	}
	defer purego.Dlclose(lib)
	for _, s := range nvmlSymbols {
		if _, err := purego.Dlsym(lib, s); err != nil {
			return nil, fmt.Errorf("%s missing: driver too old", s)
		}
	}
	var api nvmlAPI
	purego.RegisterLibFunc(&api.init, lib, "nvmlInit_v2")
	purego.RegisterLibFunc(&api.shutdown, lib, "nvmlShutdown")
	purego.RegisterLibFunc(&api.count, lib, "nvmlDeviceGetCount_v2")
	purego.RegisterLibFunc(&api.handle, lib, "nvmlDeviceGetHandleByIndex_v2")
	purego.RegisterLibFunc(&api.name, lib, "nvmlDeviceGetName")
	purego.RegisterLibFunc(&api.memory, lib, "nvmlDeviceGetMemoryInfo_v2")
	purego.RegisterLibFunc(&api.driver, lib, "nvmlSystemGetDriverVersion")
	purego.RegisterLibFunc(&api.compute, lib, "nvmlDeviceGetCudaComputeCapability")
	return queryNVML(api)
}

func dxgiGPUs() ([]GPU, error) { return nil, nil }
