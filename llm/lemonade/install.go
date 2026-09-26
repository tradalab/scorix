package lemonade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tradalab/scorix/llm/internal/install"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

const repo = "lemonade-sdk/lemonade"

var errNoBuild = errors.New("no embeddable build")

type Installer struct {
	Store *model.Store
	// Nil uses GitHub directly.
	Releases *model.GitHubReleases
	// A release such as "v11.9.0". An unpinned installer resolves the latest on
	// every Ensure, so it never finds an install offline.
	Tag string
}

// The embeddable build: a few MB, made to ship inside another app. The MSI and
// packages install a system service instead.
func assetSuffix(m probe.Machine) (string, error) {
	switch m.OS + "/" + m.Arch {
	case "windows/amd64":
		return "-windows-x64.zip", nil
	case "linux/amd64":
		return "-ubuntu-x64.tar.gz", nil
	case "linux/arm64":
		return "-ubuntu-arm64.tar.gz", nil
	case "darwin/arm64":
		return "-macos-arm64.tar.gz", nil
	}
	return "", fmt.Errorf("lemonade: %s/%s: %w", m.OS, m.Arch, errNoBuild)
}

func (in *Installer) dir(tag string) string {
	return filepath.Join(in.Store.Root, "lemonade", tag)
}

func (in *Installer) Installed() (string, bool) {
	if in.Tag == "" {
		return "", false
	}
	exe, err := install.Find(in.dir(in.Tag), serverName())
	return exe, err == nil
}

// lemond for this machine, installed the first time.
func (in *Installer) Ensure(ctx context.Context, m probe.Machine, progress func(model.Progress)) (string, error) {
	if exe, ok := in.Installed(); ok {
		return exe, nil
	}
	suffix, err := assetSuffix(m)
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
	// The asset name carries the version, so it is matched rather than built.
	for _, f := range snap.Files {
		if strings.HasPrefix(f.Path, "lemonade-embeddable-") && strings.HasSuffix(f.Path, suffix) {
			return install.Assets(ctx, in.Store, gh, snap, []string{f.Path}, in.dir(snap.Revision), serverName(), progress)
		}
	}
	return "", fmt.Errorf("lemonade: %s has no lemonade-embeddable-*%s: %w", snap.Revision, suffix, errNoBuild)
}

func serverName() string {
	if runtime.GOOS == "windows" {
		return "lemond.exe"
	}
	return "lemond"
}
