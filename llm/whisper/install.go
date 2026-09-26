package whisper

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tradalab/scorix/llm/internal/install"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

const repo = "ggml-org/whisper.cpp"

type Installer struct {
	Store *model.Store
	// Nil uses GitHub directly.
	Releases *model.GitHubReleases
	// A release such as "v1.9.1". Pin it: whisper.cpp tags releases without
	// binaries (v1.9.4 has none), so "latest" can be a release with nothing to
	// install.
	Tag string
}

// The CPU builds only: whisper's small models transcribe in real time on a
// CPU, and the CUDA build is 650 MB.
func asset(m probe.Machine) (string, error) {
	switch m.OS + "/" + m.Arch {
	case "windows/amd64":
		return "whisper-bin-x64.zip", nil
	case "windows/386":
		return "whisper-bin-Win32.zip", nil
	case "linux/amd64":
		return "whisper-bin-ubuntu-x64.tar.gz", nil
	case "linux/arm64":
		return "whisper-bin-ubuntu-arm64.tar.gz", nil
	}
	return "", fmt.Errorf("whisper: %s/%s: %w", m.OS, m.Arch, errNoBuild)
}

func (in *Installer) dir(tag string) string {
	return filepath.Join(in.Store.Root, "whisper.cpp", tag, "cpu")
}

func (in *Installer) Installed() (string, bool) {
	if in.Tag == "" {
		return "", false
	}
	exe, err := install.Find(in.dir(in.Tag), cliName())
	return exe, err == nil
}

// whisper-cli for this machine, installed the first time.
func (in *Installer) Ensure(ctx context.Context, m probe.Machine, progress func(model.Progress)) (string, error) {
	if exe, ok := in.Installed(); ok {
		return exe, nil
	}
	a, err := asset(m)
	if err != nil {
		return "", err
	}
	gh := in.Releases
	if gh == nil {
		gh = &model.GitHubReleases{}
	}
	ref := repo
	if in.Tag != "" {
		ref += "@" + in.Tag
	}
	snap, err := gh.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	return install.Assets(ctx, in.Store, gh, snap, []string{a}, in.dir(snap.Revision), cliName(), progress)
}
