package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/tradalab/scorix/internal/cli/template"
)

const (
	dockerNDKVersion = "27.2.12479018"
	dockerNDKRelease = "r27c"
	dockerPlatform   = "android-36" // compileSdk in scorix.gradle
	dockerSDKMount   = "/opt/android-sdk"
)

// By name only, so a password never lands on the docker command line or in `ps`.
var dockerPassEnv = []string{
	"SCORIX_ANDROID_KEYSTORE", "SCORIX_ANDROID_KEYSTORE_PASSWORD", "SCORIX_ANDROID_KEY_ALIAS", "SCORIX_ANDROID_KEY_PASSWORD",
}

// Read on the host: `go env -w` writes under a HOME the container lacks. By name too, as a GOPROXY URL can hold a token.
var dockerGoEnv = []string{"GOPROXY", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOSUMDB", "GOINSECURE", "GOFLAGS", "GOEXPERIMENT"}

type dockerMount struct {
	Path     string
	ReadOnly bool
}

// Every host path is mounted at the same path inside, so go.mod replaces, the keystore
// and the paths in the result all mean the same thing on both sides.
type dockerRun struct {
	Image    string
	Exe      string // this scorix binary, run again inside
	UID      int
	GID      int
	Mounts   []dockerMount
	SDK      string
	Cache    string
	GOROOT   string
	ModCache string
	Dir      string
	Env      []string // NAME=value, for values that are not secrets
	Pass     []string // NAME=value set on the docker client and handed on by name
}

func (d dockerRun) args(command ...string) []string {
	a := []string{"run", "--rm", "--init", "-u", fmt.Sprintf("%d:%d", d.UID, d.GID)}
	for _, m := range d.Mounts {
		a = append(a, "--mount", bindSpec(m.Path, m.Path, m.ReadOnly))
	}
	a = append(a, "--mount", bindSpec(d.SDK, dockerSDKMount, false))
	env := append([]string{
		"ANDROID_HOME=" + dockerSDKMount,
		"HOME=" + filepath.Join(d.Cache, "home"),
		"GOMODCACHE=" + d.ModCache,
		"GOCACHE=" + filepath.Join(d.Cache, "go"),
		"GRADLE_USER_HOME=" + filepath.Join(d.Cache, "gradle"),
		// GOROOT is already the toolchain go.mod picked; a download would land outside the mounts.
		"GOTOOLCHAIN=local",
		"PATH=" + filepath.Join(d.GOROOT, "bin") + ":/opt/gradle/bin:/opt/java/openjdk/bin:/usr/local/bin:/usr/bin:/bin",
	}, d.Env...)
	for _, e := range env {
		a = append(a, "-e", e)
	}
	for _, name := range dockerPassEnv {
		if _, ok := os.LookupEnv(name); ok {
			a = append(a, "-e", name)
		}
	}
	for _, kv := range d.Pass {
		name, _, _ := strings.Cut(kv, "=")
		a = append(a, "-e", name)
	}
	a = append(a, "-w", d.Dir, d.Image)
	return append(a, command...)
}

// --mount, not -v: -v splits on ':', and --mount reads CSV, so quoting covers ',' as well.
func bindSpec(src, dst string, readOnly bool) string {
	rec := []string{"type=bind", "source=" + src, "target=" + dst}
	if readOnly {
		rec = append(rec, "readonly")
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(rec)
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

func (d dockerRun) run(ctx context.Context, stdout io.Writer, command ...string) error {
	cmd := exec.CommandContext(ctx, "docker", d.args(command...)...)
	cmd.Env = append(os.Environ(), d.Pass...)
	cmd.Stdout, cmd.Stderr = stdout, os.Stderr
	// SIGKILL on the client would leave the container running; docker relays SIGINT to it.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	return cmd.Run()
}

// A path inside another mount is folded into it, which stays writable if either was. Sorting puts
// a parent first but not always next to its child ("/a-b" sorts between "/a" and "/a/c").
func dedupeMounts(ms []dockerMount) []dockerMount {
	slices.SortFunc(ms, func(a, b dockerMount) int { return strings.Compare(a.Path, b.Path) })
	var out []dockerMount
	for _, m := range ms {
		i := slices.IndexFunc(out, func(p dockerMount) bool { return within(m.Path, p.Path) })
		if i < 0 {
			out = append(out, m)
			continue
		}
		out[i].ReadOnly = out[i].ReadOnly && m.ReadOnly
	}
	return out
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func mobileBuildDocker(ctx context.Context, opt MobileOptions, res *MobileResult) error {
	if opt.Bundle && !opt.Release {
		return UsageError(errors.New("--aab needs --release: an app bundle is what goes to the store"))
	}
	// The container runs this very binary, and the NDK we install has only a linux-x86_64 build.
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return UsageError(fmt.Errorf("--docker needs a linux/amd64 host, this is %s/%s", runtime.GOOS, runtime.GOARCH))
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return exitErrorf(ExitMissing, "missing_prerequisite", "docker not found - install it, or drop --docker and use a local Android SDK")
	}
	bc, err := resolveBuildContext(opt.Dir, "android", "arm64")
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(bc.Root, "android", "settings.gradle.kts")); err != nil {
		return exitErrorf(ExitFailed, "not_initialized", "no Android project in %s - run `scorix mobile init android` first", filepath.Join(bc.Root, "android"))
	}
	d, err := dockerSetup(ctx, bc.Root)
	if err != nil {
		return err
	}
	if err := ensureDockerSDK(ctx, d); err != nil {
		return err
	}
	// The image has no node, so the frontend is built out here and the inner build reuses .scorix/dist.
	if opt.SkipFrontend {
		fmt.Println("==> Skipping frontend build (using existing .scorix/dist)")
	} else if err := buildFrontend(ctx, bc); err != nil {
		return err
	}
	if err := ensureDist(bc); err != nil {
		return err
	}

	inner := []string{d.Exe, "mobile", "build", "android", "--dir", bc.Root, "--skip-frontend", "--json"}
	if opt.Release {
		inner = append(inner, "--release")
	}
	if opt.Bundle {
		inner = append(inner, "--aab")
	}
	if len(opt.ABIs) > 0 {
		inner = append(inner, "--abi", strings.Join(opt.ABIs, ","))
	}
	fmt.Printf("==> Building in docker (%s)\n", d.Image)
	var stdout bytes.Buffer
	runErr := d.run(ctx, &stdout, inner...)
	return readInnerResult(stdout.Bytes(), runErr, res)
}

// readInnerResult adopts the container's --json envelope as this command's own result.
func readInnerResult(out []byte, runErr error, res *MobileResult) error {
	var env struct {
		OK    bool            `json:"ok"`
		Exit  int             `json:"exit"`
		Kind  string          `json:"kind"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		if runErr != nil {
			return fmt.Errorf("build in docker failed: %w", runErr)
		}
		return fmt.Errorf("build in docker printed no result: %w", err)
	}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, res); err != nil {
			return fmt.Errorf("read the docker build result: %w", err)
		}
	}
	if !env.OK {
		return &ExitError{Code: env.Exit, Kind: env.Kind, Err: errors.New(env.Error)}
	}
	return nil
}

func dockerSetup(ctx context.Context, root string) (dockerRun, error) {
	goenv, err := hostGoEnv(ctx, root, append([]string{"GOROOT", "GOMODCACHE"}, dockerGoEnv...))
	if err != nil {
		return dockerRun{}, err
	}
	goroot, modcache := goenv["GOROOT"], goenv["GOMODCACHE"]
	var pass []string
	for _, name := range dockerGoEnv {
		if v := goenv[name]; v != "" {
			pass = append(pass, name+"="+v)
		}
	}
	cache := os.Getenv("SCORIX_ANDROID_CACHE")
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return dockerRun{}, err
		}
		cache = filepath.Join(base, "scorix", "android")
	}
	if cache, err = filepath.Abs(cache); err != nil {
		return dockerRun{}, err
	}
	for _, sub := range []string{"android-sdk", "go", "gradle", "home"} {
		if err := os.MkdirAll(filepath.Join(cache, sub), 0o755); err != nil {
			return dockerRun{}, err
		}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return dockerRun{}, fmt.Errorf("locate the scorix binary to run in the container: %w", err)
	}
	mounts := []dockerMount{{Path: root}, {Path: cache}, {Path: modcache}, {Path: goroot, ReadOnly: true}, {Path: exe, ReadOnly: true}}
	locals, err := localReplaceDirs(ctx, root, modcache)
	if err != nil {
		return dockerRun{}, err
	}
	for _, dir := range locals {
		mounts = append(mounts, dockerMount{Path: dir, ReadOnly: true})
	}
	// Gradle's file() reads a relative keystore against android/app, and it may climb out of the project.
	if ks := os.Getenv("SCORIX_ANDROID_KEYSTORE"); ks != "" {
		if !filepath.IsAbs(ks) {
			ks = filepath.Join(root, "android", "app", ks)
		}
		mounts = append(mounts, dockerMount{Path: filepath.Dir(ks), ReadOnly: true})
	}
	image, err := ensureDockerImage(ctx)
	if err != nil {
		return dockerRun{}, err
	}
	return dockerRun{
		Image:    image,
		Exe:      exe,
		UID:      os.Getuid(),
		GID:      os.Getgid(),
		Mounts:   dedupeMounts(mounts),
		SDK:      filepath.Join(cache, "android-sdk"),
		Cache:    cache,
		GOROOT:   goroot,
		ModCache: modcache,
		Dir:      root,
		Pass:     pass,
	}, nil
}

func goOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, toolchainError(err, stderr.String())
	}
	return b, nil
}

func hostGoEnv(ctx context.Context, root string, names []string) (map[string]string, error) {
	b, err := goOutput(ctx, root, append([]string{"env", "-json"}, names...)...)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("read `go env -json`: %w", err)
	}
	return env, nil
}

// Directory replaces may point outside the project, so they need mounts of their own.
func localReplaceDirs(ctx context.Context, root, modcache string) ([]string, error) {
	b, err := goOutput(ctx, root, "list", "-m", "-f", "{{if .Replace}}{{.Replace.Dir}}{{end}}", "all")
	if err != nil {
		return nil, fmt.Errorf("list go.mod replaces: %w", err)
	}
	var dirs []string
	for _, dir := range strings.Split(string(b), "\n") {
		if dir != "" && !within(dir, modcache) {
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

// Tagged by the Dockerfile's hash, so a CLI with a changed Dockerfile builds its own image.
func ensureDockerImage(ctx context.Context) (string, error) {
	src, err := template.ReadFile(template.MobileDockerfile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(src))
	image := "scorix-android:" + hex.EncodeToString(sum[:])[:12]
	if exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil {
		return image, nil
	}
	fmt.Printf("==> Building image %s (once per Dockerfile)\n", image)
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", image, "-")
	cmd.Stdin = strings.NewReader(src)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker build %s: %w", image, err)
	}
	return image, nil
}

func ensureDockerSDK(ctx context.Context, d dockerRun) error {
	_, errNDK := os.Stat(filepath.Join(d.SDK, "ndk", dockerNDKVersion, "source.properties"))
	_, errPlatform := os.Stat(filepath.Join(d.SDK, "platforms", dockerPlatform))
	if errNDK == nil && errPlatform == nil {
		return nil
	}
	src, err := template.ReadFile(template.MobileDockerSDK)
	if err != nil {
		return err
	}
	script := filepath.Join(d.Cache, "sdk.sh")
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		return err
	}
	fmt.Printf("==> Installing the Android SDK into %s (once, a few GB)\n", d.SDK)
	d.Env = append(d.Env, "NDK_VER="+dockerNDKVersion, "NDK_REL="+dockerNDKRelease, "PLATFORM="+dockerPlatform)
	if err := d.run(ctx, os.Stdout, "sh", script); err != nil {
		return fmt.Errorf("install the Android SDK: %w", err)
	}
	return nil
}
