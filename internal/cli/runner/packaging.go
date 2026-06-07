package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Packager turns a freshly built binary (at BuildContext.TempDir) into a native
// installer/bundle, returning the path to the produced artifact.
type Packager interface {
	Package(ctx context.Context, bc *BuildContext) (string, error)
}

// PackageOptions controls `scorix package`.
type PackageOptions struct {
	Dir          string
	OS           string
	Arch         string
	Format       string
	Tags         []string
	SkipFrontend bool
	SkipSign     bool
	ForceSign    bool
}

// Package builds and packages the app into a native installer for each resolved
// target. Native installers (MSI/DMG/AppImage) require their own OS toolchain,
// so by default it packages for the host OS only.
func Package(ctx context.Context, opt PackageOptions) error {
	root, err := filepath.Abs(orDefault(opt.Dir, "."))
	if err != nil {
		return err
	}
	targets, err := resolvePackageTargets(root, opt)
	if err != nil {
		return err
	}

	var artifacts []string
	for _, t := range targets {
		fmt.Printf("\n==> Packaging %s/%s (%s)\n", t.OS, t.Arch, orDefault(t.Format, defaultFormat(t.OS)))
		art, err := packageOne(ctx, opt, t)
		if err != nil {
			return fmt.Errorf("package %s/%s: %w", t.OS, t.Arch, err)
		}
		artifacts = append(artifacts, art)
	}

	fmt.Println("\n==> Done. Artifacts:")
	for _, a := range artifacts {
		fmt.Printf("    %s\n", a)
	}
	return nil
}

func packageOne(ctx context.Context, opt PackageOptions, t PackageTarget) (string, error) {
	p, err := packagerFor(t.OS)
	if err != nil {
		return "", err
	}

	bc, err := resolveBuildContext(opt.Dir, t.OS, t.Arch)
	if err != nil {
		return "", err
	}
	bc.Format = orDefault(t.Format, defaultFormat(t.OS))
	bc.SkipSign = opt.SkipSign
	bc.ForceSign = opt.ForceSign
	if len(opt.Tags) > 0 {
		bc.Tags = opt.Tags
	}

	if _, err := buildBinary(ctx, bc, opt.SkipFrontend, ""); err != nil {
		return "", err
	}
	art, err := p.Package(ctx, bc)
	if err != nil {
		return "", err
	}
	// The staged binary in TempDir is intermediate once the installer is built.
	_ = os.RemoveAll(bc.TempDir)
	return art, nil
}

// resolvePackageTargets decides what to package: explicit flags win; otherwise
// the scorix.yaml package.targets filtered to the host OS; otherwise host.
func resolvePackageTargets(root string, opt PackageOptions) ([]PackageTarget, error) {
	if opt.OS != "" || opt.Arch != "" || opt.Format != "" {
		return []PackageTarget{{
			OS:     orDefault(opt.OS, runtime.GOOS),
			Arch:   orDefault(opt.Arch, runtime.GOARCH),
			Format: opt.Format,
		}}, nil
	}

	host := runtime.GOOS
	if cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml")); err == nil && cfg.Package != nil {
		var matched []PackageTarget
		for _, t := range cfg.Package.Targets {
			if t.OS == host {
				if t.Arch == "" {
					t.Arch = runtime.GOARCH
				}
				matched = append(matched, t)
			}
		}
		if len(matched) > 0 {
			return matched, nil
		}
		if len(cfg.Package.Targets) > 0 {
			fmt.Printf("note: scorix.yaml defines targets but none match the host OS (%s); packaging host instead. Run on the target OS or pass --os.\n", host)
		}
	}
	return []PackageTarget{{OS: host, Arch: runtime.GOARCH}}, nil
}

func packagerFor(goos string) (Packager, error) {
	switch goos {
	case "windows":
		return windowsPackager{}, nil
	case "linux":
		return linuxPackager{}, nil
	case "darwin":
		return darwinPackager{}, nil
	default:
		return nil, fmt.Errorf("packaging for %q is not supported (available: windows, linux, darwin)", goos)
	}
}

func scaffoldData(bc *BuildContext) map[string]string {
	return map[string]string{
		"Name":         bc.ProductName,
		"Identifier":   bc.Identifier,
		"Version":      bc.Version,
		"Manufacturer": bc.Manufacturer,
	}
}

func defaultFormat(goos string) string {
	switch goos {
	case "windows":
		return "msi"
	case "darwin":
		return "dmg"
	case "linux":
		return "appimage"
	default:
		return ""
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
