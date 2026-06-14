package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
)

const modulePath = "github.com/tradalab/scorix"

var (
	cliVersion string
	cliCommit  string
	cliDate    string
)

type VersionInfo struct {
	Version, Commit, Date, Go, OS, Arch string
}

func Version() VersionInfo {
	vi := VersionInfo{
		Version: cliVersion, Commit: cliCommit, Date: cliDate,
		Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if vi.Version == "" {
			vi.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if vi.Commit == "" {
					vi.Commit = s.Value
				}
			case "vcs.time":
				if vi.Date == "" {
					vi.Date = s.Value
				}
			}
		}
	}
	if vi.Version == "" || vi.Version == "(devel)" {
		vi.Version = "dev"
	}
	return vi
}

func (vi VersionInfo) Short() string {
	if vi.Commit == "" {
		return vi.Version
	}
	c := vi.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	return vi.Version + " (" + c + ")"
}

func PrintVersion() {
	vi := Version()
	fmt.Printf("scorix %s\n", vi.Short())
	if vi.Date != "" {
		fmt.Printf("  built:   %s\n", vi.Date)
	}
	fmt.Printf("  go:      %s\n", vi.Go)
	fmt.Printf("  os/arch: %s/%s\n", vi.OS, vi.Arch)
}

// Upgrade reinstalls the CLI via `go install <module>/cmd/scorix@<ref>`.
// ref defaults to "latest" (highest release tag); "main" tracks the dev tip.
func Upgrade(ctx context.Context, ref string) error {
	if ref == "" {
		ref = "latest"
	}
	target := fmt.Sprintf("%s/cmd/scorix@%s", modulePath, ref)
	fmt.Printf("==> go install %s\n", target)
	c := exec.CommandContext(ctx, "go", "install", target)
	c.Stdout, c.Stderr, c.Env = os.Stdout, os.Stderr, os.Environ()
	if err := c.Run(); err != nil {
		return fmt.Errorf("go install %s: %w", target, err)
	}
	fmt.Println("==> upgraded — run `scorix version` to confirm")
	return nil
}
