package llamacpp

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/tradalab/scorix/llm/internal/install"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

const repo = "ggml-org/llama.cpp"

type Installer struct {
	// Downloads go through it and installs live under its Root.
	Store *model.Store
	// Nil uses GitHub directly.
	Releases *model.GitHubReleases
	// A release such as "b9934". Empty follows the latest, which changes under
	// the app between two starts; an app pins it.
	Tag     string
	Options Options
}

func (in *Installer) releases() *model.GitHubReleases {
	if in.Releases != nil {
		return in.Releases
	}
	return &model.GitHubReleases{}
}

func (in *Installer) dir(tag, backend string) string {
	return filepath.Join(in.Store.Root, "llama.cpp", tag, backend)
}

// What Choose would pick, in order, told apart without a release listing:
// enough to find an install while offline.
func preferred(m probe.Machine, opt Options) []string {
	if m.OS == "darwin" {
		return []string{"metal"}
	}
	var out []string
	// The same test Choose applies: a card or driver too old for any CUDA
	// build means Vulkan was installed, and offline that is what to find.
	if opt.CUDA && m.OS == "windows" && cudaFits(m, 12) {
		out = append(out, "cuda-*")
	}
	if len(m.GPUs) > 0 {
		out = append(out, "vulkan")
	}
	return append(out, "cpu")
}

// Only the first preference counts: a CPU build left from before the GPU
// arrived is not what this machine should run now. An unpinned installer finds
// nothing, since installs are filed under the tag they resolved to.
func (in *Installer) Installed(m probe.Machine) (string, bool) {
	first := preferred(m, in.Options)[0]
	dirs, _ := filepath.Glob(in.dir(in.Tag, first))
	slices.Sort(dirs)
	for i := len(dirs) - 1; i >= 0; i-- {
		// The installer's own working directories match the same pattern and
		// sort after the real one, so a leftover would be run instead of the
		// install that works - every launch, including right after a reinstall.
		if install.Scratch(dirs[i]) {
			continue
		}
		if exe, err := install.Find(dirs[i], serverName()); err == nil {
			return exe, true
		}
	}
	return "", false
}

// The llama-server for this machine, downloaded, verified and unpacked the
// first time and found on disk after that.
func (in *Installer) Ensure(ctx context.Context, m probe.Machine, progress func(model.Progress)) (string, error) {
	if exe, ok := in.Installed(m); ok {
		return exe, nil
	}
	ref := repo
	if in.Tag != "" {
		ref += "@" + in.Tag
	}
	gh := in.releases()
	snap, err := gh.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	names := make([]string, len(snap.Files))
	for i, f := range snap.Files {
		names[i] = f.Path
	}
	b, err := Choose(m, names, in.Options)
	if err != nil {
		return "", err
	}
	return install.Assets(ctx, in.Store, gh, snap, b.Assets, in.dir(snap.Revision, b.Backend), serverName(), progress)
}

func serverName() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}
