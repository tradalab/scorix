package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// PackageConfig is the optional `package:` block in scorix.yaml, driving
// `scorix build`/`package`. Secrets are never stored here — only the names of
// the env vars that carry them.
type PackageConfig struct {
	Manufacturer string          `yaml:"manufacturer"`
	Description  string          `yaml:"description"`
	Icon         string          `yaml:"icon"` // source icon (.ico/.icns/.png); defaults to app.icon
	Ldflags      []string        `yaml:"ldflags"`
	HelpURL      string          `yaml:"help_url"`
	AboutURL     string          `yaml:"about_url"`
	Targets      []PackageTarget `yaml:"targets"`
	Windows      *WindowsPackage `yaml:"windows"`
	Sign         *SignConfig     `yaml:"sign"`
	Update       *UpdateConfig   `yaml:"update"`
}

type PackageTarget struct {
	OS     string `yaml:"os"`     // windows | darwin | linux
	Arch   string `yaml:"arch"`   // amd64 | arm64 | universal (darwin)
	Format string `yaml:"format"` // msi | dmg | appimage (defaults per-OS)
}

type WindowsPackage struct {
	// UpgradeCode is the stable MSI UpgradeCode GUID; if empty it is derived
	// deterministically from the app identifier so major-upgrades keep working.
	UpgradeCode string `yaml:"upgrade_code"`
	// Wxs lists the WiX source files (default: installer/windows/{product,ui}.wxs,
	// scaffolded on first package if missing).
	Wxs []string `yaml:"wxs"`
}

// appMetadata is the subset of the `app:` section the packager needs. scorix.yaml
// is the single source of truth for app name/version/identifier.
type appMetadata struct {
	App struct {
		Name        string   `yaml:"name"`
		Version     string   `yaml:"version"`
		Identifier  string   `yaml:"identifier"`
		Description string   `yaml:"description"`
		Icon        string   `yaml:"icon"`
		Authors     []string `yaml:"authors"`
	} `yaml:"app"`
}

type BuildContext struct {
	Root         string
	ModulePath   string
	AppName      string
	ProductName  string
	Version      string
	Identifier   string
	Manufacturer string
	Description  string
	HelpURL      string
	AboutURL     string
	IconPath     string // absolute path to source icon, or "" if none

	OS     string
	Arch   string
	Format string

	SkipSign  bool
	ForceSign bool // treat signing as enabled even if config has enabled:false (CI opt-in)

	Ldflags []string
	Tags    []string

	DistDir      string // <root>/.scorix/dist — embedded into the binary
	ShellDir     string // <root>/shell
	ShellDistSrc string // <root>/shell/dist — frontend build output
	ArtifactDir  string // <root>/artifacts — final installers land here
	TempDir      string // <root>/.scorix/<name>-<ver>-<os>-<arch>
	BinaryName   string // ProductName(.exe on windows)

	cfg *ProjectConfig
	pkg *PackageConfig
}

type BuildOptions struct {
	Dir          string
	OS           string
	Arch         string
	Output       string
	Tags         []string
	SkipFrontend bool
}

func Build(ctx context.Context, opt BuildOptions) error {
	bc, err := resolveBuildContext(opt.Dir, opt.OS, opt.Arch)
	if err != nil {
		return err
	}
	if len(opt.Tags) > 0 {
		bc.Tags = opt.Tags
	}
	out, err := buildBinary(ctx, bc, opt.SkipFrontend, opt.Output)
	if err != nil {
		return err
	}
	fmt.Printf("==> Built %s (%s/%s)\n", out, bc.OS, bc.Arch)
	return nil
}

func buildBinary(ctx context.Context, bc *BuildContext, skipFrontend bool, output string) (string, error) {
	if err := os.MkdirAll(bc.TempDir, 0o755); err != nil {
		return "", err
	}
	if skipFrontend {
		fmt.Println("==> Skipping frontend build (using existing .scorix/dist)")
	} else if err := buildFrontend(ctx, bc); err != nil {
		return "", err
	}
	if err := ensureDist(bc); err != nil {
		return "", err
	}

	out := output
	if out == "" {
		out = filepath.Join(bc.TempDir, bc.BinaryName)
	}
	if err := goBuild(ctx, bc, out); err != nil {
		return "", err
	}
	return out, nil
}

func resolveBuildContext(dir, goos, goarch string) (*BuildContext, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "scorix.yaml")); err != nil {
		return nil, fmt.Errorf("scorix.yaml not found in %s", root)
	}
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load scorix.yaml: %w", err)
	}
	meta, err := loadAppMetadata(root)
	if err != nil {
		return nil, err
	}
	modPath, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}

	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	pkg := cfg.Package
	if pkg == nil {
		pkg = &PackageConfig{}
	}

	appName := firstNonEmpty(meta.App.Name, filepath.Base(root))
	version := firstNonEmpty(meta.App.Version, "0.0.0")
	var firstAuthor string
	if len(meta.App.Authors) > 0 {
		firstAuthor = meta.App.Authors[0]
	}
	manufacturer := firstNonEmpty(pkg.Manufacturer, firstAuthor, appName)
	description := firstNonEmpty(pkg.Description, meta.App.Description, appName)

	icon := firstNonEmpty(pkg.Icon, meta.App.Icon)
	iconPath := ""
	if icon != "" {
		iconPath = icon
		if !filepath.IsAbs(iconPath) {
			iconPath = filepath.Join(root, icon)
		}
	}

	binaryName := appName
	if goos == "windows" {
		binaryName += ".exe"
	}

	bc := &BuildContext{
		Root:         root,
		ModulePath:   modPath,
		AppName:      appName,
		ProductName:  appName,
		Version:      version,
		Identifier:   meta.App.Identifier,
		Manufacturer: manufacturer,
		Description:  description,
		HelpURL:      pkg.HelpURL,
		AboutURL:     pkg.AboutURL,
		IconPath:     iconPath,
		OS:           goos,
		Arch:         goarch,
		Ldflags:      pkg.Ldflags,
		DistDir:      filepath.Join(root, ".scorix", "dist"),
		ShellDir:     filepath.Join(root, "shell"),
		ShellDistSrc: filepath.Join(root, "shell", "dist"),
		ArtifactDir:  filepath.Join(root, "artifacts"),
		BinaryName:   binaryName,
		cfg:          cfg,
		pkg:          pkg,
	}
	bc.TempDir = filepath.Join(root, ".scorix", fmt.Sprintf("%s-%s-%s-%s", appName, version, goos, goarch))
	if cfg.Build != nil {
		bc.Tags = cfg.Build.Tags
	}
	return bc, nil
}

func loadAppMetadata(root string) (*appMetadata, error) {
	scx := filepath.Join(root, "scorix.yaml")
	if m, err := readAppMetadata(scx); err == nil && m.App.Name != "" {
		return m, nil
	}
	if legacy := filepath.Join(root, "etc", "app.yaml"); fileExists(legacy) {
		return readAppMetadata(legacy)
	}
	// No legacy file — report against scorix.yaml, the expected source.
	return readAppMetadata(scx)
}

func readAppMetadata(path string) (*appMetadata, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var m appMetadata
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return &m, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (bc *BuildContext) upgradeCode() string {
	if bc.pkg.Windows != nil && bc.pkg.Windows.UpgradeCode != "" {
		return bc.pkg.Windows.UpgradeCode
	}
	seed := firstNonEmpty(bc.Identifier, bc.ModulePath+"/"+bc.AppName)
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("scorix:upgrade:"+seed)).String()
}

func buildFrontend(ctx context.Context, bc *BuildContext) error {
	if _, err := os.Stat(filepath.Join(bc.ShellDir, "package.json")); err != nil {
		fmt.Println("==> No shell/package.json — skipping frontend build")
		return nil
	}
	fmt.Println("==> Building frontend (pnpm build)...")
	cmd := exec.CommandContext(ctx, "pnpm", "build")
	cmd.Dir = bc.ShellDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("frontend build failed: %w", err)
	}
	if _, err := os.Stat(bc.ShellDistSrc); err != nil {
		return fmt.Errorf("frontend output not found at %s (check shell build outDir)", bc.ShellDistSrc)
	}
	fmt.Printf("==> Copying %s -> %s\n", bc.ShellDistSrc, bc.DistDir)
	if err := os.RemoveAll(bc.DistDir); err != nil {
		return err
	}
	return copyDir(bc.ShellDistSrc, bc.DistDir)
}

// ensureDist guarantees .scorix/dist exists and is non-empty so the binary's
// //go:embed all:.scorix/dist directive resolves even when frontend is skipped.
func ensureDist(bc *BuildContext) error {
	return ensureEmbedDir(bc.DistDir)
}

// ensureEmbedDir guarantees distDir exists and is non-empty so a binary's
// //go:embed all:.scorix/dist directive compiles. Needed wherever Go is invoked
// before a frontend exists — `scorix build --skip-frontend` and `scorix dev`
// (which serves the HMR window but still has to compile the embed).
func ensureEmbedDir(distDir string) error {
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Printf("warning: %s is empty — using a placeholder (no bundled frontend)\n", distDir)
		return os.WriteFile(filepath.Join(distDir, ".keep"), nil, 0o644)
	}
	return nil
}

func goBuild(ctx context.Context, bc *BuildContext, out string) error {
	if bc.OS == "darwin" && strings.EqualFold(bc.Arch, "universal") {
		return goBuildDarwinUniversal(ctx, bc, out)
	}
	return goBuildArch(ctx, bc, bc.Arch, out)
}

func goBuildArch(ctx context.Context, bc *BuildContext, goarch, out string) error {
	var sysoPath string
	if bc.OS == "windows" && bc.IconPath != "" && strings.EqualFold(filepath.Ext(bc.IconPath), ".ico") {
		// Go links every *.syso in the dir; a project's own one would collide with
		// ours (".rsrc merge failure: duplicate leaf"), so defer to theirs.
		if existing, _ := filepath.Glob(filepath.Join(bc.Root, "*.syso")); len(existing) > 0 {
			fmt.Println("==> Using project's existing .syso resource (skipping generated icon)")
		} else if p, err := genWindowsIcon(ctx, bc); err != nil {
			fmt.Printf("warning: windows icon resource skipped: %v\n", err)
		} else {
			sysoPath = p
			defer os.Remove(sysoPath)
		}
	}

	args := []string{"build"}
	if ld := buildLdflags(bc); ld != "" {
		args = append(args, "-ldflags", ld)
	}
	if len(bc.Tags) > 0 {
		args = append(args, "-tags", strings.Join(bc.Tags, ","))
	}
	args = append(args, "-o", out, ".")

	env := append(os.Environ(), "GOOS="+bc.OS, "GOARCH="+goarch)
	if bc.OS == "darwin" {
		// CGO (sqlite/webkit) required; per-arch CC override (CC_amd64/CC_arm64) for cross-arch.
		env = append(env, "CGO_ENABLED=1")
		if cc := os.Getenv("CC_" + goarch); cc != "" {
			env = append(env, "CC="+cc)
		}
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = bc.Root
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("==> GOOS=%s GOARCH=%s go %s\n", bc.OS, goarch, strings.Join(args, " "))
	return cmd.Run()
}

func goBuildDarwinUniversal(ctx context.Context, bc *BuildContext, out string) error {
	lipo, err := exec.LookPath("lipo")
	if err != nil {
		return fmt.Errorf("lipo not found in PATH — universal darwin builds require macOS / Xcode command line tools")
	}
	amd := out + "-amd64"
	arm := out + "-arm64"
	fmt.Println("==> Building darwin universal (amd64 + arm64)")
	if err := goBuildArch(ctx, bc, "amd64", amd); err != nil {
		return err
	}
	if err := goBuildArch(ctx, bc, "arm64", arm); err != nil {
		return err
	}
	defer os.Remove(amd)
	defer os.Remove(arm)

	cmd := exec.CommandContext(ctx, lipo, "-create", "-output", out, amd, arm)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("==> lipo -create -output %s %s %s\n", out, amd, arm)
	return cmd.Run()
}

func buildLdflags(bc *BuildContext) string {
	var f []string
	if bc.OS == "windows" {
		f = append(f, "-H=windowsgui")
	}
	f = append(f, bc.Ldflags...)
	return strings.Join(f, " ")
}

// genWindowsIcon generates an icon .syso via akavel/rsrc; caller removes it.
func genWindowsIcon(ctx context.Context, bc *BuildContext) (string, error) {
	syso := filepath.Join(bc.Root, "scorix_app_windows.syso")
	cmd := exec.CommandContext(ctx, "go", "run", "github.com/akavel/rsrc@latest", "-ico", bc.IconPath, "-o", syso)
	cmd.Dir = bc.Root
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return syso, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
